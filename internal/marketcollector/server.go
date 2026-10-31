package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

// The queue is deliberately bounded. A stalled consumer must eventually
// reconnect and let the existing REST/sequence-gap backfill repair the gap;
// keeping an unbounded book-event queue would exhaust the small host.
const clientQueueSize = 4096

type Server struct {
	socket  string
	log     *slog.Logger
	health  *exchange.HealthRegistry
	mu      sync.Mutex
	clients map[*serverClient]Subscription
	updates chan struct{}
}

type serverClient struct {
	conn net.Conn
	send chan []byte
	done chan struct{}
}

func NewServer(socket string, health *exchange.HealthRegistry, log *slog.Logger) *Server {
	if health == nil {
		health = exchange.NewHealthRegistry()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{socket: socket, health: health, log: log, clients: make(map[*serverClient]Subscription), updates: make(chan struct{}, 1)}
}

func (s *Server) Run(ctx context.Context) error {
	if s == nil || s.socket == "" {
		return errors.New("market collector socket is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.socket), 0750); err != nil {
		return fmt.Errorf("create collector socket directory: %w", err)
	}
	if info, err := os.Lstat(s.socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("collector socket path is not a socket: %s", s.socket)
		}
		if err := os.Remove(s.socket); err != nil {
			return fmt.Errorf("remove stale collector socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect collector socket: %w", err)
	}
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("listen on collector socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.socket)
	}()
	if err := os.Chmod(s.socket, 0660); err != nil {
		return fmt.Errorf("set collector socket permissions: %w", err)
	}

	go s.runStreams(ctx)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept collector client: %w", acceptErr)
		}
		go s.serveClient(ctx, conn)
	}
}

func (s *Server) serveClient(ctx context.Context, conn net.Conn) {
	client := &serverClient{conn: conn, send: make(chan []byte, clientQueueSize), done: make(chan struct{})}
	s.addClient(client)
	defer func() {
		close(client.done)
		s.removeClient(client)
		_ = conn.Close()
	}()

	writerDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-client.done:
				return
			case payload := <-client.send:
				if _, err := conn.Write(append(payload, '\n')); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	decoder := json.NewDecoder(conn)
	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil && s.log != nil {
				s.log.Debug("market collector client disconnected", "error", err)
			}
			break
		}
		if req.Type != requestSubscribe {
			continue
		}
		s.updateClient(client, req.Subscription.normalized())
	}
	_ = conn.Close()
	<-writerDone
}

func (s *Server) addClient(client *serverClient) {
	s.mu.Lock()
	s.clients[client] = Subscription{}
	s.mu.Unlock()
	s.notifyUpdate()
}

func (s *Server) removeClient(client *serverClient) {
	s.mu.Lock()
	delete(s.clients, client)
	s.mu.Unlock()
	s.notifyUpdate()
}

func (s *Server) updateClient(client *serverClient, sub Subscription) {
	s.mu.Lock()
	if _, ok := s.clients[client]; ok {
		s.clients[client] = sub
	}
	s.mu.Unlock()
	s.notifyUpdate()
}

func (s *Server) notifyUpdate() {
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

func (s *Server) unionSubscription() Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Subscription
	aggregate, depth := map[string]struct{}{}, map[string]struct{}{}
	for _, sub := range s.clients {
		out.Liquidations = out.Liquidations || sub.Liquidations
		for _, symbol := range sub.AggregateSymbols {
			aggregate[symbol] = struct{}{}
		}
		for _, symbol := range sub.DepthSymbols {
			depth[symbol] = struct{}{}
		}
	}
	for symbol := range aggregate {
		out.AggregateSymbols = append(out.AggregateSymbols, symbol)
	}
	for symbol := range depth {
		out.DepthSymbols = append(out.DepthSymbols, symbol)
	}
	return out.normalized()
}

func (s *Server) broadcast(event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	symbol := event.Symbol
	if symbol == "" {
		switch event.Type {
		case eventLiquidation:
			if event.Liquidation != nil {
				symbol = event.Liquidation.Symbol
			}
		case eventAggregate:
			if event.AggregateTrade != nil {
				symbol = event.AggregateTrade.Symbol
			}
		case eventBook:
			if event.Book != nil {
				symbol = event.Book.Symbol
			}
		}
	}

	s.mu.Lock()
	recipients := make([]*serverClient, 0, len(s.clients))
	for client, sub := range s.clients {
		if event.Type == eventLiquidation && sub.Liquidations ||
			event.Type == eventAggregate && containsSymbol(symbol, sub.AggregateSymbols) ||
			event.Type == eventBook && containsSymbol(symbol, sub.DepthSymbols) {
			recipients = append(recipients, client)
		}
	}
	s.mu.Unlock()
	for _, client := range recipients {
		select {
		case client.send <- payload:
		case <-client.done:
		}
	}
	return nil
}

func (s *Server) runStreams(ctx context.Context) {
	var current Subscription
	var aggregateCancel, depthCancel context.CancelFunc
	var aggregateUpdates chan []string
	liquidationCtx, liquidationCancel := context.WithCancel(ctx)
	defer liquidationCancel()
	liquidationStarted := false
	for {
		select {
		case <-ctx.Done():
			if aggregateCancel != nil {
				aggregateCancel()
			}
			if depthCancel != nil {
				depthCancel()
			}
			return
		case <-s.updates:
			next := s.unionSubscription()
			if next.Liquidations && !liquidationStarted {
				liquidationStarted = true
				exchange.StartLiquidationStreams(liquidationCtx, nil, func(ctx context.Context, event domain.LiquidationEvent) error {
					return s.broadcast(Event{Type: eventLiquidation, Symbol: event.Symbol, Liquidation: &event})
				}, s.health, s.log)
			}
			if equalSymbols(current.AggregateSymbols, next.AggregateSymbols) && equalSymbols(current.DepthSymbols, next.DepthSymbols) {
				current = next
				continue
			}
			if aggregateCancel == nil && len(next.AggregateSymbols) > 0 {
				streamCtx, cancel := context.WithCancel(ctx)
				aggregateCancel = cancel
				aggregateUpdates = make(chan []string, 1)
				exchange.StartDynamicAggregateTradeStreams(streamCtx, s.currentAggregateSymbols, aggregateUpdates, func(ctx context.Context, trade domain.AggregateTrade) error {
					return s.broadcast(Event{Type: eventAggregate, Symbol: trade.Symbol, AggregateTrade: &trade})
				}, s.health, s.log)
			} else if aggregateUpdates != nil && !equalSymbols(current.AggregateSymbols, next.AggregateSymbols) {
				select {
				case <-aggregateUpdates:
				default:
				}
				select {
				case aggregateUpdates <- next.AggregateSymbols:
				default:
				}
			}
			if depthCancel != nil {
				depthCancel()
				depthCancel = nil
			}
			if len(next.DepthSymbols) > 0 {
				streamCtx, cancel := context.WithCancel(ctx)
				depthCancel = cancel
				exchange.StartDepthStreams(streamCtx, next.DepthSymbols, func(ctx context.Context, book domain.BookSnapshot) error {
					return s.broadcast(Event{Type: eventBook, Symbol: book.Symbol, Book: &book})
				}, s.health, s.log)
			}
			current = next
		}
	}
}

func (s *Server) currentAggregateSymbols() []string {
	return s.unionSubscription().AggregateSymbols
}

func equalSymbols(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
