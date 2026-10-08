package exchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type LiquidationSink func(context.Context, domain.LiquidationEvent) error

const binanceMarketStreamBase = "wss://fstream.binance.com/market"

func StartLiquidationStreams(ctx context.Context, symbols []string, sink LiquidationSink, health *HealthRegistry, log *slog.Logger) {
	go reconnect(ctx, "binance_liquidations", .65, health, log, func(ctx context.Context) error { return runBinance(ctx, symbols, sink, health) })
}

func reconnect(ctx context.Context, name string, coverage float64, h *HealthRegistry, log *slog.Logger, run func(context.Context) error) {
	backoff := time.Second
	for ctx.Err() == nil {
		log.Info("liquidation stream connecting", "source", name)
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}
		h.Fail(name, err)
		wait := backoff + time.Duration(rand.Int63n(int64(backoff/2+1)))
		log.Warn("liquidation stream disconnected", "source", name, "error", err, "retry_after", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > time.Minute {
			backoff = time.Minute
		}
		h.Set(name, Health{Connected: false, LastError: err.Error(), Coverage: coverage})
	}
}

func dial(ctx context.Context, url string) (*websocket.Conn, error) {
	c, _, e := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if e != nil {
		return nil, e
	}
	c.SetReadLimit(4 << 20)
	_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			}
		}
	}()
	return c, nil
}
func allowed(s string, symbols []string) bool {
	for _, x := range symbols {
		if s == x {
			return true
		}
	}
	return false
}

func runBinance(ctx context.Context, symbols []string, sink LiquidationSink, h *HealthRegistry) error {
	c, e := dial(ctx, binanceMarketStreamBase+"/ws/!forceOrder@arr")
	if e != nil {
		return e
	}
	defer c.Close()
	h.Set("binance_liquidations", Health{Connected: true, Coverage: .65})
	deadline := time.Now().Add(23*time.Hour + 50*time.Minute)
	for time.Now().Before(deadline) {
		_, b, e := c.ReadMessage()
		if e != nil {
			return e
		}
		ev, ok := parseBinanceLiquidation(b, symbols)
		if !ok {
			continue
		}
		if e = sink(ctx, ev); e != nil {
			return e
		}
		h.Touch("binance_liquidations", .65)
	}
	return fmt.Errorf("proactive 24h reconnect")
}

func runBybit(ctx context.Context, symbols []string, sink LiquidationSink, h *HealthRegistry) error {
	c, e := dial(ctx, "wss://stream.bybit.com/v5/public/linear")
	if e != nil {
		return e
	}
	defer c.Close()
	args := make([]string, len(symbols))
	for i, s := range symbols {
		args[i] = "allLiquidation." + s
	}
	if e = c.WriteJSON(map[string]any{"op": "subscribe", "args": args}); e != nil {
		return e
	}
	h.Touch("bybit_liquidations", 1)
	for {
		_, b, e := c.ReadMessage()
		if e != nil {
			return e
		}
		for _, ev := range parseBybitLiquidations(b, symbols) {
			if e = sink(ctx, ev); e != nil {
				return e
			}
			h.Touch("bybit_liquidations", 1)
		}
	}
}

func runOKX(ctx context.Context, symbols []string, sink LiquidationSink, h *HealthRegistry) error {
	ctVal := map[string]float64{"BTCUSDT": .01, "ETHUSDT": .1}
	for _, symbol := range symbols {
		var meta struct {
			Data []map[string]string `json:"data"`
		}
		if newHTTP().get(ctx, "https://www.okx.com/api/v5/public/instruments?instType=SWAP&instId="+okxSymbol(symbol), &meta) == nil && len(meta.Data) > 0 {
			if value := f(meta.Data[0]["ctVal"]); value > 0 {
				ctVal[symbol] = value
			}
		}
	}
	c, e := dial(ctx, "wss://ws.okx.com/ws/v5/public")
	if e != nil {
		return e
	}
	defer c.Close()
	args := make([]map[string]string, 0, len(symbols))
	for _, s := range symbols {
		args = append(args, map[string]string{"channel": "liquidation-orders", "instType": "SWAP", "instId": okxSymbol(s)})
	}
	if e = c.WriteJSON(map[string]any{"op": "subscribe", "args": args}); e != nil {
		return e
	}
	h.Touch("okx_liquidations", .9)
	for {
		_, b, e := c.ReadMessage()
		if e != nil {
			return e
		}
		for _, ev := range parseOKXLiquidations(b, symbols, ctVal) {
			if e = sink(ctx, ev); e != nil {
				return e
			}
			h.Touch("okx_liquidations", .9)
		}
	}
}

func parseBinanceLiquidation(b []byte, symbols []string) (domain.LiquidationEvent, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(b, &envelope) != nil {
		return domain.LiquidationEvent{}, false
	}
	if data, ok := envelope["data"]; ok {
		return parseBinanceLiquidation(data, symbols)
	}
	var streamType int
	_ = json.Unmarshal(envelope["st"], &streamType)
	if streamType != 0 && streamType != 1 {
		return domain.LiquidationEvent{}, false
	}
	var event int64
	var order map[string]any
	if json.Unmarshal(envelope["E"], &event) != nil || json.Unmarshal(envelope["o"], &order) != nil {
		return domain.LiquidationEvent{}, false
	}
	symbol := anyString(order["s"])
	if symbol == "" || (len(symbols) > 0 && !allowed(symbol, symbols)) {
		return domain.LiquidationEvent{}, false
	}
	price := f(anyString(order["ap"]))
	if price == 0 {
		price = f(anyString(order["p"]))
	}
	qty := f(anyString(order["q"]))
	if filled := f(anyString(order["z"])); filled > 0 {
		qty = filled
	}
	if price <= 0 || qty <= 0 || math.IsNaN(price) || math.IsInf(price, 0) || math.IsNaN(qty) || math.IsInf(qty, 0) || (anyString(order["S"]) != "BUY" && anyString(order["S"]) != "SELL") {
		return domain.LiquidationEvent{}, false
	}
	ts := ms(order["T"])
	if ts.UnixMilli() <= 0 {
		ts = time.UnixMilli(event).UTC()
	}
	if ts.UnixMilli() <= 0 {
		return domain.LiquidationEvent{}, false
	}
	return newEvent("binance", symbol, binancePositionSide(anyString(order["S"])), ts, price, qty, price*qty, "sampled"), true
}

func parseBybitLiquidations(b []byte, symbols []string) []domain.LiquidationEvent {
	var m struct {
		Topic string           `json:"topic"`
		Data  []map[string]any `json:"data"`
	}
	if json.Unmarshal(b, &m) != nil || !strings.HasPrefix(m.Topic, "allLiquidation.") {
		return nil
	}
	var out []domain.LiquidationEvent
	for _, x := range m.Data {
		symbol := anyString(x["s"])
		if !allowed(symbol, symbols) {
			continue
		}
		price, qty := f(anyString(x["p"])), f(anyString(x["v"]))
		if price <= 0 || qty == 0 {
			continue
		}
		out = append(out, newEvent("bybit", symbol, bybitPositionSide(anyString(x["S"])), ms(x["T"]), price, qty, price*qty, "complete"))
	}
	return out
}

func parseOKXLiquidations(b []byte, symbols []string, ctVal map[string]float64) []domain.LiquidationEvent {
	var m struct {
		Data []struct {
			InstID  string           `json:"instId"`
			Details []map[string]any `json:"details"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var out []domain.LiquidationEvent
	for _, d := range m.Data {
		for _, x := range d.Details {
			inst := anyString(x["instId"])
			if inst == "" {
				inst = d.InstID
			}
			symbol := strings.ReplaceAll(strings.TrimSuffix(inst, "-SWAP"), "-", "")
			if !allowed(symbol, symbols) {
				continue
			}
			price, contracts := f(anyString(x["bkPx"])), f(anyString(x["sz"]))
			side := strings.ToLower(anyString(x["posSide"]))
			if price <= 0 || contracts == 0 || (side != "long" && side != "short") {
				continue
			}
			qty := contracts * ctVal[symbol]
			out = append(out, newEvent("okx", symbol, side, ms(x["ts"]), price, qty, price*qty, "near_complete"))
		}
	}
	return out
}

func newEvent(exchange, symbol, side string, t time.Time, price, qty, notional float64, coverage string) domain.LiquidationEvent {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	raw := exchange + symbol + side + strconv.FormatInt(t.UnixMilli(), 10) + strconv.FormatFloat(price, 'g', -1, 64) + strconv.FormatFloat(qty, 'g', -1, 64)
	sum := sha256.Sum256([]byte(raw))
	return domain.LiquidationEvent{ID: hex.EncodeToString(sum[:16]), Exchange: exchange, Symbol: symbol, PositionSide: side, EventTime: t, ReceivedAt: time.Now().UTC(), Price: price, Quantity: math.Abs(qty), NotionalUSD: math.Abs(notional), Coverage: coverage}
}

func binancePositionSide(orderSide string) string {
	if strings.EqualFold(orderSide, "SELL") {
		return "long"
	}
	return "short"
}

// Bybit explicitly defines S=Buy as a liquidated long position in this stream.
func bybitPositionSide(side string) string {
	if strings.EqualFold(side, "Buy") {
		return "long"
	}
	return "short"
}
