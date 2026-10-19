package server

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestPositionEntryFillTimeLongAddAndReduce(t *testing.T) {
	entry := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	reduce := entry.Add(time.Hour)
	got, ok, errText := positionEntryFillTime(okx.Position{
		InstID:  "BTC-USDT-SWAP",
		PosSide: "long",
		Pos:     "1",
	}, []positionEntryFill{
		{InstID: "BTC-USDT-SWAP", PosSide: "long", Side: "sell", Size: 0.5, FillTime: reduce},
		{InstID: "BTC-USDT-SWAP", PosSide: "long", Side: "buy", Size: 1.5, FillTime: entry},
	})
	if !ok || !got.Equal(entry) || errText != "" {
		t.Fatalf("entry=%s ok=%v err=%q", got, ok, errText)
	}
}

func TestPositionEntryFillTimeShortAddAndReduce(t *testing.T) {
	entry := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	reduce := entry.Add(time.Hour)
	got, ok, errText := positionEntryFillTime(okx.Position{
		InstID:  "BTC-USDT-SWAP",
		PosSide: "short",
		Pos:     "1",
	}, []positionEntryFill{
		{InstID: "BTC-USDT-SWAP", PosSide: "short", Side: "buy", Size: 0.5, FillTime: reduce},
		{InstID: "BTC-USDT-SWAP", PosSide: "short", Side: "sell", Size: 1.5, FillTime: entry},
	})
	if !ok || !got.Equal(entry) || errText != "" {
		t.Fatalf("entry=%s ok=%v err=%q", got, ok, errText)
	}
}

func TestPositionEntryFillTimeReversalUsesFlipFill(t *testing.T) {
	longEntry := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	flip := longEntry.Add(time.Hour)
	got, ok, errText := positionEntryFillTime(okx.Position{
		InstID:  "BTC-USDT-SWAP",
		PosSide: "net",
		Pos:     "-1",
	}, []positionEntryFill{
		{InstID: "BTC-USDT-SWAP", Side: "sell", Size: 2, FillTime: flip},
		{InstID: "BTC-USDT-SWAP", Side: "buy", Size: 1, FillTime: longEntry},
	})
	if !ok || !got.Equal(flip) || errText != "" {
		t.Fatalf("entry=%s ok=%v err=%q", got, ok, errText)
	}
}

func TestPositionEntryFillTimeInsufficientHistory(t *testing.T) {
	_, ok, errText := positionEntryFillTime(okx.Position{
		InstID:  "BTC-USDT-SWAP",
		PosSide: "long",
		Pos:     "2",
	}, []positionEntryFill{
		{InstID: "BTC-USDT-SWAP", PosSide: "long", Side: "buy", Size: 1, FillTime: time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)},
	})
	if ok || !strings.Contains(errText, "成交不足") {
		t.Fatalf("expected insufficient history, ok=%v err=%q", ok, errText)
	}
}

func TestPositionViewWithEntryTimeFallsBackToExchangePositionTime(t *testing.T) {
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	positionTime := now.Add(-30 * time.Minute)
	view := positionViewWithEntryTime(okx.Position{
		InstID:  "BTCUSDT",
		PosSide: "net",
		Pos:     "1",
		UTime:   strconv.FormatInt(positionTime.UnixMilli(), 10),
	}, nil, now, entryTimeSourceBinanceTrade, errors.New("binance 502"))
	if view.EntryFillTime != positionTime.Format(time.RFC3339Nano) ||
		view.EntryTimeSource != entryTimeSourcePositionTime ||
		view.HoldingSeconds != int64((30*time.Minute).Seconds()) ||
		!strings.Contains(view.EntryTimeError, "binance 502") {
		t.Fatalf("bad fallback position time view: %#v", view)
	}
}

func TestPositionViewADXMetadataMatchesExactEntryOrder(t *testing.T) {
	store, err := storage.NewOrderStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	adx := 18.42
	signal := trading.Signal{
		Action:         trading.ActionShort,
		SourceAction:   trading.ActionLong,
		APIID:          "default",
		TargetExchange: trading.ExchangeOKX,
		TradeEnv:       trading.TradeEnvDemo,
		Coinpair:       "BTC",
		Ticker:         "BTCUSDT",
		PositionEffect: trading.PositionEffectOpen,
		PositionSide:   trading.PositionSideShort,
		ADX:            &adx,
		MarketStrategy: trading.MarketStrategyScalp,
	}
	record, duplicate, err := store.RecordAccepted(signal, storage.DedupeKey(signal), now)
	if err != nil || duplicate {
		t.Fatalf("record accepted duplicate=%v err=%v", duplicate, err)
	}
	if err := store.MarkSubmitted(record.SignalID, trading.OrderResult{
		APIID:          "default",
		TargetExchange: trading.ExchangeOKX,
		InstID:         "BTC-USDT-SWAP",
		OrdID:          "entry-100",
		PositionEffect: trading.PositionEffectOpen,
		PositionSide:   trading.PositionSideShort,
	}, now); err != nil {
		t.Fatal(err)
	}

	entryTime := now.Add(-time.Hour)
	views := []positionView{positionViewWithEntryTime(okx.Position{
		InstID:  "BTC-USDT-SWAP",
		PosSide: "short",
		Pos:     "1",
	}, []positionEntryFill{{
		InstID:   "BTC-USDT-SWAP",
		OrdID:    "entry-100",
		PosSide:  "short",
		Side:     "sell",
		Size:     1,
		FillTime: entryTime,
	}}, now, entryTimeSourceOKXFills, nil)}
	srv := &Server{Orders: store}
	srv.enrichPositionViewsWithOrderMetadata(views, trading.ExchangeOKX, "default")
	if views[0].MarketStrategy != trading.MarketStrategyScalp || views[0].EntryADX == nil || *views[0].EntryADX != adx {
		t.Fatalf("position metadata was not matched: %#v", views[0])
	}

	views[0].entryOrdID = "manual-order"
	views[0].MarketStrategy = ""
	views[0].EntryADX = nil
	srv.enrichPositionViewsWithOrderMetadata(views, trading.ExchangeOKX, "default")
	if views[0].MarketStrategy != "" || views[0].EntryADX != nil {
		t.Fatalf("manual position should not inherit old metadata: %#v", views[0])
	}
}

func TestTVBotUIIncludesPositionADXColumns(t *testing.T) {
	for _, marker := range []string{
		`{ id: "strategy", title: "策略"`,
		`{ id: "entry_adx", title: "入场 ADX"`,
		`function positionMarketStrategyText(strategy)`,
		`if (value === "scalp") return "震荡行情"`,
		`function positionEntryADXCell(row)`,
	} {
		if !strings.Contains(tvbotHTML, marker) {
			t.Fatalf("tvbot UI missing %q", marker)
		}
	}
}
