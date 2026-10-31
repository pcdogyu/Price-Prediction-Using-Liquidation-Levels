package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type AggregateTradeSink func(context.Context, domain.AggregateTrade) error

func StartAggregateTradeStreams(ctx context.Context, symbols []string, sink AggregateTradeSink, health *HealthRegistry, log *slog.Logger) {
	go reconnect(ctx, "binance_agg_trades", 1, health, log, func(ctx context.Context) error {
		return runBinanceAggregateTrades(ctx, symbols, sink, health)
	})
}

// StartDynamicAggregateTradeStreams keeps one Binance WebSocket alive while
// the caller changes the subscribed symbols. This avoids reconnecting every
// existing symbol when a signal requests one more on-demand profile. The
// provider is also consulted after a reconnect so the latest subscription is
// restored even when the update was already consumed before the disconnect.
func StartDynamicAggregateTradeStreams(ctx context.Context, symbols func() []string, updates <-chan []string, sink AggregateTradeSink, health *HealthRegistry, log *slog.Logger) {
	go reconnect(ctx, "binance_agg_trades", 1, health, log, func(ctx context.Context) error {
		return runDynamicBinanceAggregateTrades(ctx, symbols, updates, sink, health)
	})
}

func runBinanceAggregateTrades(ctx context.Context, symbols []string, sink AggregateTradeSink, health *HealthRegistry) error {
	streams := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@aggTrade")
	}
	c, err := dial(ctx, binanceMarketStreamBase+"/stream?streams="+strings.Join(streams, "/"))
	if err != nil {
		return err
	}
	defer c.Close()
	health.Set("binance_agg_trades", Health{Connected: true, Coverage: 1})
	deadline := time.Now().Add(23*time.Hour + 50*time.Minute)
	for time.Now().Before(deadline) {
		_, payload, err := c.ReadMessage()
		if err != nil {
			return err
		}
		trade, ok := ParseBinanceAggregateTrade(payload, symbols)
		if !ok {
			continue
		}
		if err = sink(ctx, trade); err != nil {
			return err
		}
		health.Touch("binance_agg_trades", 1)
	}
	return fmt.Errorf("proactive 24h reconnect")
}

func runDynamicBinanceAggregateTrades(ctx context.Context, symbols func() []string, updates <-chan []string, sink AggregateTradeSink, health *HealthRegistry) error {
	c, err := dial(ctx, binanceMarketStreamBase+"/ws")
	if err != nil {
		return err
	}
	defer c.Close()
	current := normalizeAggregateSymbols(symbols())
	if err = updateAggregateSubscriptions(c, nil, current); err != nil {
		return err
	}
	health.Set("binance_agg_trades", Health{Connected: true, Coverage: 1})

	frames := make(chan []byte, 8192)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, payload, readError := c.ReadMessage()
			if readError != nil {
				readErr <- readError
				return
			}
			select {
			case frames <- payload:
			case <-ctx.Done():
				return
			}
		}
	}()
	deadline := time.NewTimer(23*time.Hour + 50*time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case next := <-updates:
			next = normalizeAggregateSymbols(next)
			if equalAggregateSymbols(current, next) {
				continue
			}
			if err = updateAggregateSubscriptions(c, current, next); err != nil {
				return err
			}
			current = next
		case <-deadline.C:
			return fmt.Errorf("proactive 24h reconnect")
		case payload := <-frames:
			trade, ok := ParseBinanceAggregateTrade(payload, current)
			if !ok {
				continue
			}
			if err = sink(ctx, trade); err != nil {
				return err
			}
			health.Touch("binance_agg_trades", 1)
		}
	}
}

func updateAggregateSubscriptions(c *websocket.Conn, old, next []string) error {
	params := make([]string, 0, len(next))
	for _, symbol := range next {
		if !allowed(symbol, old) {
			params = append(params, strings.ToLower(symbol)+"@aggTrade")
		}
	}
	if len(params) > 0 {
		if err := c.WriteJSON(map[string]any{"method": "SUBSCRIBE", "params": params, "id": time.Now().UnixNano()}); err != nil {
			return err
		}
	}
	removed := make([]string, 0, len(old))
	for _, symbol := range old {
		if !allowed(symbol, next) {
			removed = append(removed, strings.ToLower(symbol)+"@aggTrade")
		}
	}
	if len(removed) > 0 {
		if err := c.WriteJSON(map[string]any{"method": "UNSUBSCRIBE", "params": removed, "id": time.Now().UnixNano()}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAggregateSymbols(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, raw := range symbols {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; !ok {
			seen[symbol] = struct{}{}
			out = append(out, symbol)
		}
	}
	sort.Strings(out)
	return out
}

func equalAggregateSymbols(a, b []string) bool {
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

func ParseBinanceAggregateTrade(payload []byte, symbols []string) (domain.AggregateTrade, bool) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) == nil && len(envelope.Data) > 0 {
		payload = envelope.Data
	}
	var message struct {
		Event     string `json:"e"`
		EventTime int64  `json:"E"`
		Symbol    string `json:"s"`
		ID        int64  `json:"a"`
		Price     string `json:"p"`
		Qty       string `json:"q"`
		Time      int64  `json:"T"`
		Type      int    `json:"st"`
	}
	if json.Unmarshal(payload, &message) != nil || message.Event != "aggTrade" || !allowed(message.Symbol, symbols) {
		return domain.AggregateTrade{}, false
	}
	// The unified stream may append st=2 for COIN-M. Missing st is the legacy
	// USD-M payload and st=1 is explicitly USD-M.
	if message.Type != 0 && message.Type != 1 {
		return domain.AggregateTrade{}, false
	}
	price, quantity := f(message.Price), f(message.Qty)
	if message.ID <= 0 || price <= 0 || quantity <= 0 || message.Time <= 0 {
		return domain.AggregateTrade{}, false
	}
	return domain.AggregateTrade{ID: message.ID, Symbol: message.Symbol, Time: time.UnixMilli(message.Time).UTC(), Price: price, Quantity: quantity, PriceText: message.Price}, true
}

type aggregateTradeResponse struct {
	ID     int64  `json:"a"`
	Price  string `json:"p"`
	Qty    string `json:"q"`
	Time   int64  `json:"T"`
	Symbol string `json:"s"`
}

func BinanceAggregateTrades(ctx context.Context, symbol string, fromID int64, start, end time.Time) ([]domain.AggregateTrade, error) {
	values := url.Values{"symbol": {symbol}, "limit": {"1000"}}
	if fromID > 0 {
		values.Set("fromId", strconv.FormatInt(fromID, 10))
	} else {
		values.Set("startTime", strconv.FormatInt(start.UTC().UnixMilli(), 10))
		values.Set("endTime", strconv.FormatInt(end.UTC().UnixMilli(), 10))
	}
	var rows []aggregateTradeResponse
	if err := newHTTP().get(ctx, "https://fapi.binance.com/fapi/v1/aggTrades?"+values.Encode(), &rows); err != nil {
		return nil, err
	}
	out := make([]domain.AggregateTrade, 0, len(rows))
	for _, row := range rows {
		price, quantity := f(row.Price), f(row.Qty)
		if row.ID <= 0 || price <= 0 || quantity <= 0 || row.Time <= 0 {
			continue
		}
		out = append(out, domain.AggregateTrade{ID: row.ID, Symbol: symbol, Time: time.UnixMilli(row.Time).UTC(), Price: price, Quantity: quantity, PriceText: row.Price})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
