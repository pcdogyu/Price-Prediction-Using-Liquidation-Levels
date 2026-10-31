package marketcollector

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

type Handler struct {
	Liquidation    func(context.Context, domain.LiquidationEvent) error
	AggregateTrade func(context.Context, domain.AggregateTrade) error
	Book           func(context.Context, domain.BookSnapshot) error
}

type Client struct {
	socket  string
	log     *slog.Logger
	health  *exchange.HealthRegistry
	mu      sync.RWMutex
	sub     Subscription
	updates chan Subscription
}

func NewClient(socket string, health *exchange.HealthRegistry, log *slog.Logger) *Client {
	if health == nil {
		health = exchange.NewHealthRegistry()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{socket: socket, health: health, log: log, updates: make(chan Subscription, 1)}
}

func (c *Client) SetSubscription(sub Subscription) {
	if c == nil {
		return
	}
	sub = sub.normalized()
	c.mu.Lock()
	c.sub = sub
	c.mu.Unlock()
	select {
	case c.updates <- sub:
	default:
		select {
		case <-c.updates:
		default:
		}
		select {
		case c.updates <- sub:
		default:
		}
	}
}

func (c *Client) subscription() Subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sub
}

func (c *Client) Run(ctx context.Context, sub Subscription, handler Handler) {
	c.SetSubscription(sub)
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.runConnection(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.health.Fail("market_collector", err)
			if c.log != nil {
				c.log.Warn("market collector connection unavailable", "error", err, "retry_after", backoff)
			}
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (c *Client) runConnection(ctx context.Context, handler Handler) error {
	if c.socket == "" {
		return errors.New("market collector socket is required")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := c.writeSubscription(conn, c.subscription()); err != nil {
		return err
	}
	c.health.Set("market_collector", exchange.Health{Connected: true, Coverage: 1})

	events := make(chan Event, 16384)
	readErr := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(conn))
		for {
			var event Event
			if err := decoder.Decode(&event); err != nil {
				readErr <- err
				return
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				return err
			}
			return fmt.Errorf("read market collector stream: %w", err)
		case sub := <-c.updates:
			if err := c.writeSubscription(conn, sub); err != nil {
				return err
			}
		case event := <-events:
			if err := c.handleEvent(ctx, handler, event); err != nil {
				return err
			}
			c.health.Touch("market_collector", 1)
		}
	}
}

func (c *Client) writeSubscription(conn net.Conn, sub Subscription) error {
	payload, err := json.Marshal(request{Type: requestSubscribe, Subscription: sub.normalized()})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = conn.Write(payload)
	return err
}

func (c *Client) handleEvent(ctx context.Context, handler Handler, event Event) error {
	switch event.Type {
	case eventLiquidation:
		if event.Liquidation != nil && handler.Liquidation != nil {
			return handler.Liquidation(ctx, *event.Liquidation)
		}
	case eventAggregate:
		if event.AggregateTrade != nil && handler.AggregateTrade != nil {
			return handler.AggregateTrade(ctx, *event.AggregateTrade)
		}
	case eventBook:
		if event.Book != nil && handler.Book != nil {
			return handler.Book(ctx, *event.Book)
		}
	}
	return nil
}
