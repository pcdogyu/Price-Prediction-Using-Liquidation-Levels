package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTradeMonitorStoragePersistsFillsLifecycleCheckpointAndEvents(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	fills := []BinanceFill{{
		APIID:       "main",
		Symbol:      "BTCUSDT",
		TradeID:     "101",
		OrderID:     "9001",
		Side:        "BUY",
		Price:       "50000",
		Qty:         "0.01",
		RealizedPnl: "0",
		FillTime:    now.UnixMilli(),
	}}
	inserted, err := store.UpsertBinanceFills(fills, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 1 {
		t.Fatalf("expected first fill insert, got %#v", inserted)
	}
	inserted, err = store.UpsertBinanceFills(fills, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 0 {
		t.Fatalf("duplicate fill should not insert again: %#v", inserted)
	}

	rec := OrderRecord{
		SignalID:       "sig-1",
		Status:         StatusSubmitted,
		Action:         trading.ActionLong,
		APIID:          "main",
		TargetExchange: trading.ExchangeBinance,
		Coinpair:       "BTC",
		Ticker:         "BTCUSDT",
		Amount:         "100",
		Leverage:       10,
		AcceptedAt:     now,
		UpdatedAt:      now,
		Result: trading.OrderResult{
			APIID:          "main",
			TargetExchange: trading.ExchangeBinance,
			InstID:         "BTCUSDT",
			OrdID:          "9001",
			ClOrdID:        "entry-1",
			RiskOrders: []trading.RiskOrderResult{
				{Exchange: trading.ExchangeBinance, AlgoID: "7001", ClientAlgoID: "entry-1TP", OrderType: "TAKE_PROFIT_MARKET", TriggerPrice: "51000"},
				{Exchange: trading.ExchangeBinance, AlgoID: "7002", ClientAlgoID: "entry-1SL", OrderType: "STOP_MARKET", TriggerPrice: "49500"},
			},
		},
	}
	lifecycle, err := store.UpsertTradeLifecycleFromOrder(rec, "", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Status != TradeLifecycleEntryPending || lifecycle.Symbol != "BTCUSDT" || len(lifecycle.SLAlgoIDs) != 1 || lifecycle.SLAlgoIDs[0] != "7002" {
		t.Fatalf("bad lifecycle: %#v", lifecycle)
	}
	lifecycle, err = store.UpdateTradeLifecycle(lifecycle.LifecycleID, TradeLifecycleUpdate{
		Status:       TradeLifecycleOpen,
		EntryPrice:   "50000",
		EntryQty:     "0.01",
		LastFillTime: now.UnixMilli(),
		UpdatedAt:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Status != TradeLifecycleOpen || lifecycle.EntryPrice != "50000" {
		t.Fatalf("lifecycle update failed: %#v", lifecycle)
	}
	lifecycles, err := store.ListTradeLifecycles(TradeMonitorFilter{Exchange: trading.ExchangeBinance, APIID: "main", Symbol: "BTCUSDT"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifecycles) != 1 || lifecycles[0].LifecycleID != "sig-1" {
		t.Fatalf("bad lifecycle list: %#v", lifecycles)
	}

	cp := TradeMonitorCheckpoint{
		Exchange:     trading.ExchangeBinance,
		APIID:        "main",
		Symbol:       "BTCUSDT",
		LastFillTime: now.UnixMilli(),
		LastTradeID:  "101",
		LastPolledAt: now,
		UpdatedAt:    now,
	}
	if err := store.UpsertTradeMonitorCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	gotCP, found, err := store.TradeMonitorCheckpoint(trading.ExchangeBinance, "main", "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if !found || gotCP.LastTradeID != "101" {
		t.Fatalf("bad checkpoint found=%v cp=%#v", found, gotCP)
	}

	event := TradeMonitorEvent{
		EventTime:      now,
		Exchange:       trading.ExchangeBinance,
		APIID:          "main",
		Symbol:         "BTCUSDT",
		LifecycleID:    "sig-1",
		SourceSignalID: "sig-1",
		EventType:      "lifecycle_open",
		Status:         TradeLifecycleOpen,
		Message:        "opened",
	}
	if err := store.InsertTradeMonitorEvent(event); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListTradeMonitorEvents(TradeMonitorFilter{Status: TradeLifecycleOpen}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != "lifecycle_open" {
		t.Fatalf("bad event list: %#v", events)
	}
}
