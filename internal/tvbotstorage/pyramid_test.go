package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPyramidStatePersistsAndIsIsolated(t *testing.T) {
	store, err := NewOrderStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	state := PyramidState{
		TargetExchange: "BINANCE",
		TradeEnv:       "DEMO",
		APIID:          "account-a",
		Coinpair:       "btcusdt",
		PositionSide:   "LONG",
		Level:          2,
		LastEntryPrice: 105,
		UpdatedAt:      now,
	}
	if err := store.UpsertPyramidState(state); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetPyramidState(PyramidStateKey{TargetExchange: "binance", TradeEnv: "demo", APIID: "account-a", Coinpair: "BTCUSDT", PositionSide: "long"})
	if err != nil || !found {
		t.Fatalf("state lookup found=%v err=%v", found, err)
	}
	if got.Level != 2 || got.LastEntryPrice != 105 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected state: %#v", got)
	}
	if _, found, err := store.GetPyramidState(PyramidStateKey{TargetExchange: "binance", TradeEnv: "demo", APIID: "account-b", Coinpair: "BTCUSDT", PositionSide: "long"}); err != nil || found {
		t.Fatalf("pyramid state should be isolated by API account: found=%v err=%v", found, err)
	}
	if err := store.DeletePyramidState(PyramidStateKey{TargetExchange: "binance", TradeEnv: "demo", APIID: "account-a", Coinpair: "BTCUSDT", PositionSide: "long"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetPyramidState(PyramidStateKey{TargetExchange: "binance", TradeEnv: "demo", APIID: "account-a", Coinpair: "BTCUSDT", PositionSide: "long"}); err != nil || found {
		t.Fatalf("deleted pyramid state still exists: found=%v err=%v", found, err)
	}
}

func TestPyramidStatePersistsInSQLite(t *testing.T) {
	store, err := NewOrderStore(filepath.Join(t.TempDir(), "orders.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := PyramidState{
		TargetExchange: "okx",
		TradeEnv:       "demo",
		APIID:          "account-a",
		Coinpair:       "BTCUSDT",
		PositionSide:   "short",
		Level:          3,
		LastEntryPrice: 95,
		UpdatedAt:      time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
	}
	if err := store.UpsertPyramidState(state); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetPyramidState(PyramidStateKey{TargetExchange: "okx", TradeEnv: "demo", APIID: "account-a", Coinpair: "BTCUSDT", PositionSide: "short"})
	if err != nil || !found || got.Level != 3 || got.LastEntryPrice != 95 {
		t.Fatalf("sqlite pyramid state=%#v found=%v err=%v", got, found, err)
	}
}
