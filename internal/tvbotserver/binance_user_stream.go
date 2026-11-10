package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	trading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

var binanceUserStreamRegistry = struct {
	sync.Mutex
	running   map[string]struct{}
	active    map[string]struct{}
	positions map[string]binanceUserPositionSnapshot
}{
	running:   map[string]struct{}{},
	active:    map[string]struct{}{},
	positions: map[string]binanceUserPositionSnapshot{},
}

const (
	// A live user stream snapshot is preferred for the positions page. Keep a
	// shorter freshness window for normal reads, but retain a bounded stale
	// window so a brief Binance REST/WebSocket outage does not erase a usable
	// account snapshot from the dashboard.
	binanceUserStreamPositionCacheTTL = 10 * time.Minute
	binanceStalePositionCacheTTL      = 30 * time.Minute
	binanceUserStreamReadTimeout      = 10 * time.Minute
)

type binanceUserPositionSnapshot struct {
	updatedAt time.Time
	positions []tvbotbinance.Position
}

func (s *Server) startBinanceUserDataStreams(ctx context.Context) {
	if s == nil || s.ConfigStore == nil || s.Orders == nil {
		return
	}
	cfg := s.ConfigStore.Get()
	if s.primaryExchangeForConfig(cfg) != trading.ExchangeBinance {
		return
	}
	store := s.binanceCredentialStore(cfg.Trading.Env)
	if store == nil {
		return
	}
	for _, requestedAPIID := range configuredBinanceAPIIDs(store.Status()) {
		requestedAPIID := requestedAPIID
		go s.runBinanceUserDataStream(ctx, requestedAPIID)
	}
}

func (s *Server) runBinanceUserDataStream(ctx context.Context, requestedAPIID string) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		cfg := s.ConfigStore.Get()
		if s.primaryExchangeForConfig(cfg) != trading.ExchangeBinance {
			return
		}
		client, apiID, err := s.binanceClientForCredentials(cfg, requestedAPIID)
		if err == nil {
			streamID := binanceUserStreamID(client, apiID)
			if claimBinanceUserStream(streamID) {
				err = s.runBinanceUserDataConnection(ctx, cfg, client, apiID, streamID)
				releaseBinanceUserStream(streamID)
				if ctx.Err() != nil {
					return
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		wait := binanceUserStreamRetryWait(err, backoff)
		if s.Logger != nil && err != nil {
			s.Logger.Warn("Binance user data stream unavailable", "api_id", apiID, "error", err, "retry_after", wait.String())
		}
		if !waitContext(ctx, wait) {
			return
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (s *Server) runBinanceUserDataConnection(ctx context.Context, cfg tvbotconfig.Config, client tvbotbinance.Client, apiID, streamID string) error {
	listenKey, err := client.StartUserDataStream(ctx)
	if err != nil {
		return err
	}
	streamURL := tvbotbinance.UserDataStreamURL(client.BaseURL, listenKey)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, streamURL, nil)
	if err != nil {
		return fmt.Errorf("connect Binance user data stream: %w", err)
	}
	defer conn.Close()
	markBinanceUserStreamActive(streamID, true)
	defer markBinanceUserStreamActive(streamID, false)

	if s.Logger != nil {
		s.Logger.Info("Binance user data stream connected", "api_id", apiID)
	}
	conn.SetReadLimit(1 << 20)
	configureBinanceUserStreamConnection(conn)
	keepaliveCtx, cancelKeepalive := context.WithCancel(ctx)
	defer cancelKeepalive()
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	keepaliveErr := make(chan error, 1)
	go keepBinanceUserDataStreamAlive(keepaliveCtx, client, listenKey, keepaliveErr)
	go closeBinanceUserStreamOnContext(streamCtx, conn)

	for {
		messageType, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return fmt.Errorf("read Binance user data stream: %w", readErr)
		}
		if err := refreshBinanceUserStreamReadDeadline(conn); err != nil {
			return fmt.Errorf("refresh Binance user data stream read deadline: %w", err)
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if positions, ok := tvbotbinance.ParseUserDataPositions(payload); ok {
			updateBinanceUserStreamPositions(streamID, positions, s.now())
		}
		if strings.Contains(string(payload), `"e":"listenKeyExpired"`) {
			return fmt.Errorf("Binance user data listenKey expired")
		}
		trade, ok := tvbotbinance.ParseUserDataTrade(payload)
		if !ok {
			continue
		}
		cfg = s.ConfigStore.Get()
		if err := s.ingestBinanceUserDataTrade(ctx, cfg, client, apiID, trade, s.now()); err != nil {
			if s.Logger != nil {
				s.Logger.Warn("failed to persist Binance WebSocket fill", "api_id", apiID, "symbol", trade.Symbol, "error", err)
			}
		}
		select {
		case keepErr := <-keepaliveErr:
			return keepErr
		default:
		}
	}
}

func refreshBinanceUserStreamReadDeadline(conn *websocket.Conn) error {
	return conn.SetReadDeadline(time.Now().Add(binanceUserStreamReadTimeout))
}

func configureBinanceUserStreamConnection(conn *websocket.Conn) {
	_ = refreshBinanceUserStreamReadDeadline(conn)
	conn.SetPingHandler(func(appData string) error {
		if err := refreshBinanceUserStreamReadDeadline(conn); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})
	conn.SetPongHandler(func(string) error {
		return refreshBinanceUserStreamReadDeadline(conn)
	})
}

func keepBinanceUserDataStreamAlive(ctx context.Context, client tvbotbinance.Client, listenKey string, errCh chan<- error) {
	ticker := time.NewTicker(25 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.KeepAliveUserDataStream(ctx, listenKey); err != nil {
				select {
				case errCh <- fmt.Errorf("keepalive Binance user data stream: %w", err):
				default:
				}
				return
			}
		}
	}
}

func closeBinanceUserStreamOnContext(ctx context.Context, conn *websocket.Conn) {
	<-ctx.Done()
	_ = conn.Close()
}

func (s *Server) ingestBinanceUserDataTrade(ctx context.Context, cfg tvbotconfig.Config, client tvbotbinance.Client, apiID string, trade tvbotbinance.UserTrade, now time.Time) error {
	fill := tvbotstorage.BinanceFill{
		APIID:           apiID,
		Symbol:          trade.Symbol,
		TradeID:         fmt.Sprintf("%d", trade.ID),
		OrderID:         fmt.Sprintf("%d", trade.OrderID),
		Side:            trade.Side,
		PositionSide:    trade.PositionSide,
		Price:           trade.Price,
		Qty:             trade.Qty,
		QuoteQty:        trade.QuoteQty,
		RealizedPnl:     trade.RealizedPnl,
		Commission:      trade.Commission,
		CommissionAsset: trade.CommissionAsset,
		Buyer:           trade.Buyer,
		Maker:           trade.Maker,
		FillTime:        trade.Time,
	}
	inserted, err := s.Orders.UpsertBinanceFills([]tvbotstorage.BinanceFill{fill}, now)
	if err != nil {
		return err
	}
	checkpoint, found, err := s.Orders.TradeMonitorCheckpoint(trading.ExchangeBinance, apiID, strings.ToUpper(strings.TrimSpace(trade.Symbol)))
	if err != nil {
		return err
	}
	if found {
		if err := s.recordBinanceFillLossCooldowns(cfg, inserted, now); err != nil {
			return err
		}
	}
	for _, insertedFill := range inserted {
		if tradeFillMonitorBinanceEnabled(cfg) {
			if err := s.processBinanceFill(ctx, cfg, client, insertedFill, now); err != nil {
				return err
			}
		}
	}
	lastFillTime := checkpoint.LastFillTime
	lastTradeID := checkpoint.LastTradeID
	if fill.FillTime > lastFillTime || (fill.FillTime == lastFillTime && fill.TradeID > lastTradeID) {
		lastFillTime = fill.FillTime
		lastTradeID = fill.TradeID
	}
	return s.Orders.UpsertTradeMonitorCheckpoint(tvbotstorage.TradeMonitorCheckpoint{
		Exchange:     trading.ExchangeBinance,
		APIID:        apiID,
		Symbol:       strings.ToUpper(strings.TrimSpace(trade.Symbol)),
		LastFillTime: lastFillTime,
		LastTradeID:  lastTradeID,
		LastPolledAt: now,
		UpdatedAt:    now,
	})
}

func binanceUserStreamID(client tvbotbinance.Client, apiID string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(client.BaseURL, "/") + "|" + apiID + "|" + client.Credentials.APIKey))
	return hex.EncodeToString(sum[:])
}

func claimBinanceUserStream(streamID string) bool {
	binanceUserStreamRegistry.Lock()
	defer binanceUserStreamRegistry.Unlock()
	if _, ok := binanceUserStreamRegistry.running[streamID]; ok {
		return false
	}
	binanceUserStreamRegistry.running[streamID] = struct{}{}
	return true
}

func releaseBinanceUserStream(streamID string) {
	binanceUserStreamRegistry.Lock()
	delete(binanceUserStreamRegistry.running, streamID)
	delete(binanceUserStreamRegistry.active, streamID)
	binanceUserStreamRegistry.Unlock()
}

func markBinanceUserStreamActive(streamID string, active bool) {
	binanceUserStreamRegistry.Lock()
	defer binanceUserStreamRegistry.Unlock()
	if active {
		binanceUserStreamRegistry.active[streamID] = struct{}{}
	} else {
		delete(binanceUserStreamRegistry.active, streamID)
	}
}

func binanceUserStreamIsActive(client tvbotbinance.Client, apiID string) bool {
	streamID := binanceUserStreamID(client, apiID)
	binanceUserStreamRegistry.Lock()
	defer binanceUserStreamRegistry.Unlock()
	_, ok := binanceUserStreamRegistry.active[streamID]
	return ok
}

func updateBinanceUserStreamPositions(streamID string, updates []tvbotbinance.Position, now time.Time) {
	binanceUserStreamRegistry.Lock()
	defer binanceUserStreamRegistry.Unlock()
	snapshot := binanceUserStreamRegistry.positions[streamID]
	byKey := make(map[string]tvbotbinance.Position, len(snapshot.positions)+len(updates))
	for _, position := range snapshot.positions {
		byKey[binanceUserPositionKey(position)] = position
	}
	for _, position := range updates {
		key := binanceUserPositionKey(position)
		if isZeroBinancePosition(position) {
			delete(byKey, key)
			continue
		}
		byKey[key] = position
	}
	merged := make([]tvbotbinance.Position, 0, len(byKey))
	for _, position := range byKey {
		merged = append(merged, position)
	}
	binanceUserStreamRegistry.positions[streamID] = binanceUserPositionSnapshot{updatedAt: now.UTC(), positions: merged}
}

func cachedBinanceUserStreamPositions(client tvbotbinance.Client, apiID string, now time.Time) ([]tvbotbinance.Position, bool) {
	positions, _, ok := cachedBinanceUserStreamPositionsAt(client, apiID, now)
	return positions, ok
}

func cachedBinanceUserStreamPositionsAt(client tvbotbinance.Client, apiID string, now time.Time) ([]tvbotbinance.Position, time.Time, bool) {
	streamID := binanceUserStreamID(client, apiID)
	binanceUserStreamRegistry.Lock()
	defer binanceUserStreamRegistry.Unlock()
	snapshot, ok := binanceUserStreamRegistry.positions[streamID]
	if !ok || now.Sub(snapshot.updatedAt) > binanceUserStreamPositionCacheTTL {
		return nil, time.Time{}, false
	}
	positions := append([]tvbotbinance.Position(nil), snapshot.positions...)
	return positions, snapshot.updatedAt, true
}

func binancePositionMonitorPositions(ctx context.Context, client tvbotbinance.Client, apiID string, now time.Time) ([]tvbotbinance.Position, error) {
	if binanceUserStreamIsActive(client, apiID) {
		if positions, _, ok := cachedBinanceUserStreamPositionsAt(client, apiID, now); ok {
			return positions, nil
		}
	}
	return client.Positions(ctx, "")
}

func staleBinanceUserStreamPositions(client tvbotbinance.Client, apiID string, now time.Time) ([]tvbotbinance.Position, time.Time, bool) {
	streamID := binanceUserStreamID(client, apiID)
	binanceUserStreamRegistry.Lock()
	defer binanceUserStreamRegistry.Unlock()
	snapshot, ok := binanceUserStreamRegistry.positions[streamID]
	if !ok || snapshot.updatedAt.IsZero() || now.Sub(snapshot.updatedAt) > binanceStalePositionCacheTTL {
		return nil, time.Time{}, false
	}
	positions := append([]tvbotbinance.Position(nil), snapshot.positions...)
	return positions, snapshot.updatedAt, true
}

func binanceUserPositionKey(position tvbotbinance.Position) string {
	return strings.ToUpper(strings.TrimSpace(position.Symbol)) + "|" + strings.ToUpper(strings.TrimSpace(position.PositionSide))
}

func isZeroBinancePosition(position tvbotbinance.Position) bool {
	raw := strings.TrimSpace(position.PositionAmt)
	if raw == "" {
		return true
	}
	value, err := strconv.ParseFloat(raw, 64)
	return err == nil && value == 0
}

func binanceUserStreamRetryWait(err error, backoff time.Duration) time.Duration {
	if until, ok := tvbotbinance.RateLimitUntil(err); ok {
		if wait := time.Until(until); wait > 0 {
			return wait
		}
	}
	if until, ok := tvbotbinance.RateLimitPauseUntil(time.Now()); ok {
		if wait := time.Until(until); wait > 0 {
			return wait
		}
	}
	if backoff <= 0 {
		return time.Second
	}
	return backoff
}

func waitContext(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
