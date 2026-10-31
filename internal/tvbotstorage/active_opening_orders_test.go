package storage

import (
	"path/filepath"
	"testing"
	"time"

	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestListActiveOpeningOrdersFiltersScopeAndSymbols(t *testing.T) {
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.new(t)
			now := time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC)
			seed := func(signal tvbottrading.Signal, status OrderStatus, key string) {
				signal.Normalize()
				record, duplicate, err := store.RecordAccepted(signal, key, now)
				if err != nil || duplicate {
					t.Fatalf("record accepted duplicate=%v err=%v", duplicate, err)
				}
				switch status {
				case StatusSubmitted:
					if err := store.MarkSubmitted(record.SignalID, tvbottrading.OrderResult{InstID: signal.Ticker}, now); err != nil {
						t.Fatal(err)
					}
				case StatusFailed:
					if err := store.MarkFailed(record.SignalID, testingError("seed failure"), now); err != nil {
						t.Fatal(err)
					}
				}
			}
			seed(tvbottrading.Signal{
				Action: tvbottrading.ActionLong, APIID: "main", TargetExchange: tvbottrading.ExchangeOKX, TradeEnv: tvbottrading.TradeEnvDemo,
				Coinpair: "BTC", Ticker: "BTCUSDT", PositionEffect: tvbottrading.PositionEffectOpen, Price: tvbottrading.NewFlexibleFloat(1), Amount: tvbottrading.NewFlexibleFloat(10), Leverage: 1,
				RawJSON: `{"condition":"aggressive_liquidation_signal","source":"liquidation_alert"}`,
			}, StatusAccepted, "other-strategy")
			seed(tvbottrading.Signal{
				Action: tvbottrading.ActionLong, APIID: "main", TargetExchange: tvbottrading.ExchangeOKX, TradeEnv: tvbottrading.TradeEnvDemo,
				Coinpair: "ETH", Ticker: "ETHUSDT", PositionEffect: tvbottrading.PositionEffectOpen, Price: tvbottrading.NewFlexibleFloat(1), Amount: tvbottrading.NewFlexibleFloat(10), Leverage: 1,
				RawJSON: `{"condition":"value_area_signal","source":"value_area_signal"}`,
			}, StatusSubmitted, "same-strategy-other-symbol")
			seed(tvbottrading.Signal{
				Action: tvbottrading.ActionLong, APIID: "backup", TargetExchange: tvbottrading.ExchangeOKX, TradeEnv: tvbottrading.TradeEnvDemo,
				Coinpair: "BTC", Ticker: "BTCUSDT", PositionEffect: tvbottrading.PositionEffectOpen, Price: tvbottrading.NewFlexibleFloat(1), Amount: tvbottrading.NewFlexibleFloat(10), Leverage: 1,
				RawJSON: `{"condition":"steady_liquidation_confirmation","source":"liquidation_alert"}`,
			}, StatusAccepted, "other-api")
			seed(tvbottrading.Signal{
				Action: tvbottrading.ActionLong, APIID: "main", TargetExchange: tvbottrading.ExchangeOKX, TradeEnv: tvbottrading.TradeEnvDemo,
				Coinpair: "BTC", Ticker: "BTCUSDT", PositionEffect: tvbottrading.PositionEffectOpen, Price: tvbottrading.NewFlexibleFloat(1), Amount: tvbottrading.NewFlexibleFloat(10), Leverage: 1,
				RawJSON: `{"condition":"value_area_signal","source":"value_area_signal"}`,
			}, StatusFailed, "failed")

			records, err := store.ListActiveOpeningOrders(ActiveOpeningOrderFilter{
				TargetExchange: tvbottrading.ExchangeOKX,
				TradeEnv:       tvbottrading.TradeEnvDemo,
				APIIDs:         []string{"main"},
				Symbols:        []string{"BTC", "BTCUSDT"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].RawJSON == "" {
				t.Fatalf("unexpected active opening records: %#v", records)
			}
		})
	}
}

type testingError string

func (e testingError) Error() string { return string(e) }
