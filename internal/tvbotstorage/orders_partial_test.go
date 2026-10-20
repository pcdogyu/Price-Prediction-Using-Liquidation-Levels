package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestMarkPartiallySubmittedPersistsLayerResults(t *testing.T) {
	tests := []struct {
		name string
		new  func(t *testing.T) *OrderStore
	}{
		{name: "memory", new: func(t *testing.T) *OrderStore {
			store, err := NewOrderStore("")
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
		{name: "sqlite", new: func(t *testing.T) *OrderStore {
			store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "orders.db"), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.new(t)
			now := time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)
			signal := trading.Signal{Action: trading.ActionLong, Coinpair: "BTC", Ticker: "BTCUSDT", Price: trading.NewFlexibleFloat(100), Amount: trading.NewFlexibleFloat(10000), Leverage: 5}
			record, duplicate, err := store.RecordAccepted(signal, "partial-dedupe", now)
			if err != nil || duplicate {
				t.Fatalf("record accepted: duplicate=%v err=%v", duplicate, err)
			}
			result := trading.OrderResult{OrdID: "entry-1", Partial: true, Layers: []trading.OrderLayerResult{{Name: "market", Status: "submitted", OrdID: "entry-1"}, {Name: "signal_limit", Status: "failed", Error: "rejected"}}}
			if err := store.MarkPartiallySubmitted(record.SignalID, result, errors.New("layer rejected"), now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			got, ok := store.Get(record.SignalID)
			if !ok || got.Status != StatusPartial || !got.Result.Partial || len(got.Result.Layers) != 2 || got.ErrorCode != "partial_submission" {
				t.Fatalf("unexpected partial record: ok=%v record=%#v", ok, got)
			}
		})
	}
}
