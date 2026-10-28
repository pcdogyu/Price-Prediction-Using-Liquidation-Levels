package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	appstore "github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestSQLiteOrderStoreCanShareApplicationDatabase(t *testing.T) {
	appDB, err := appstore.Open(filepath.Join(t.TempDir(), "liquidation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer appDB.Close()

	orders, err := NewSQLiteOrderStoreWithDB(appDB.SQLDB(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := orders.Close(); err != nil {
		t.Fatalf("shared order store close: %v", err)
	}
	if err := appDB.Ping(context.Background()); err != nil {
		t.Fatalf("closing shared order store must not close application db: %v", err)
	}
}

func TestSQLiteOrderStoreMigratesLegacyJSONOnce(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "orders.json")
	dbPath := filepath.Join(dir, "tvbot.db")
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	state := orderState{
		Dedupe: map[string]string{"dedupe": "sig-1"},
		Orders: []OrderRecord{{
			SignalID:   "sig-1",
			DedupeKey:  "dedupe",
			Status:     StatusSubmitted,
			Action:     trading.ActionLong,
			APIID:      "default",
			Coinpair:   "BTC",
			Ticker:     "BTCUSDT",
			Price:      "50000",
			Amount:     "100",
			AcceptedAt: now,
			UpdatedAt:  now,
			Result:     trading.OrderResult{InstID: "BTC-USDT-SWAP", OrdID: "123"},
		}},
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteOrderStore(dbPath, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.List(10); len(got) != 1 || got[0].SignalID != "sig-1" || got[0].Result.OrdID != "123" {
		t.Fatalf("bad migrated records: %#v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteOrderStore(dbPath, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.List(10); len(got) != 1 {
		t.Fatalf("migration should be idempotent, got %d records: %#v", len(got), got)
	}
}

func TestSQLiteOrderStoreRecordDuplicateAndMarkResults(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	tp := trading.NewFlexibleFloat(3)
	sl := trading.NewFlexibleFloat(1.5)
	adx := 18.42
	signal := trading.Signal{
		Action:         trading.ActionShort,
		SourceAction:   trading.ActionLong,
		APIID:          "backup",
		TradeEnv:       trading.TradeEnvLive,
		Coinpair:       "ETH",
		Price:          trading.NewFlexibleFloat(2500),
		SentAt:         "2026-07-24T03:00:00Z",
		Ticker:         "ETHUSDT",
		Leverage:       3,
		Amount:         trading.NewFlexibleFloat(120),
		ADX:            &adx,
		MarketStrategy: trading.MarketStrategyScalp,
		Risk: trading.Risk{
			Type:  trading.RiskTPSL,
			TPPct: &tp,
			SLPct: &sl,
		},
		OrderIntent:    "exit_long",
		PositionEffect: trading.PositionEffectClose,
		PositionSide:   trading.PositionSideLong,
		Token:          "token",
		RawJSON:        `{"action":"sell","token":"[redacted]"}`,
	}
	signal.Normalize()
	dedupe := DedupeKey(signal)
	rec, duplicate, err := store.RecordAccepted(signal, dedupe, now)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate || rec.Status != StatusAccepted {
		t.Fatalf("first record duplicate=%v rec=%#v", duplicate, rec)
	}
	if err := store.MarkSubmitted(rec.SignalID, trading.OrderResult{InstID: "ETH-USDT-SWAP", OrdID: "okx-1", PositionEffect: trading.PositionEffectClose, PositionSide: trading.PositionSideLong}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	dup, duplicate, err := store.RecordAccepted(signal, dedupe, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate || dup.Status != StatusDuplicate {
		t.Fatalf("duplicate record duplicate=%v rec=%#v", duplicate, dup)
	}
	if err := store.MarkFailed("missing", errors.New("nope"), now); err == nil {
		t.Fatal("expected missing signal error")
	}
	records := store.List(10)
	if len(records) != 2 || records[0].Status != StatusDuplicate || records[1].Status != StatusSubmitted || records[1].Result.OrdID != "okx-1" {
		t.Fatalf("bad records: %#v", records)
	}
	if records[0].RawJSON != signal.RawJSON || records[1].RawJSON != signal.RawJSON {
		t.Fatalf("raw json should be preserved: %#v", records)
	}
	if records[1].Risk.Type != trading.RiskTPSL || records[1].Risk.TPPct == nil || records[1].Risk.TPPct.Value != 3 || records[1].Risk.SLPct == nil || records[1].Risk.SLPct.Value != 1.5 {
		t.Fatalf("risk settings should be preserved: %#v", records[1].Risk)
	}
	for _, rec := range records {
		if rec.OrderIntent != "exit_long" || rec.PositionEffect != trading.PositionEffectClose || rec.PositionSide != trading.PositionSideLong || rec.TradeEnv != trading.TradeEnvLive {
			t.Fatalf("position semantics should be preserved: %#v", rec)
		}
		if rec.SourceAction != trading.ActionLong || rec.ADX == nil || *rec.ADX != adx || rec.MarketStrategy != trading.MarketStrategyScalp {
			t.Fatalf("ADX routing metadata should be preserved: %#v", rec)
		}
	}
}

func TestOrderStoreRecordIgnoredDoesNotBlockLaterAcceptance(t *testing.T) {
	run := func(t *testing.T, store *OrderStore) {
		t.Helper()
		now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
		signal := trading.Signal{
			Action:         trading.ActionLong,
			TargetExchange: trading.ExchangeOKX,
			TradeEnv:       trading.TradeEnvDemo,
			Coinpair:       "ETHUSDT.P",
			Ticker:         "OKX:ETHUSDT.P",
			Price:          trading.NewFlexibleFloat(2500),
			Amount:         trading.NewFlexibleFloat(100),
			Leverage:       5,
		}
		ignored, err := store.RecordIgnored(signal, "ETH", now)
		if err != nil {
			t.Fatal(err)
		}
		if ignored.Status != StatusIgnored || ignored.ErrorCode != "coinpair_filtered" || !strings.Contains(ignored.Error, `"ETH"`) {
			t.Fatalf("bad ignored record: %#v", ignored)
		}
		accepted, duplicate, err := store.RecordAccepted(signal, DedupeKey(signal), now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if duplicate || accepted.Status != StatusAccepted {
			t.Fatalf("ignored record should not poison dedupe: duplicate=%v record=%#v", duplicate, accepted)
		}
	}

	t.Run("memory", func(t *testing.T) {
		store, err := NewOrderStore("")
		if err != nil {
			t.Fatal(err)
		}
		run(t, store)
	})
	t.Run("sqlite", func(t *testing.T) {
		store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		run(t, store)
	})
}

func TestOrderStoreListByTargetExchangeFiltersMemoryAndSQLite(t *testing.T) {
	run := func(t *testing.T, store *OrderStore) {
		t.Helper()
		now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
		okxSignal := trading.Signal{Action: trading.ActionLong, TargetExchange: trading.ExchangeOKX, Coinpair: "BTC", Ticker: "OKX:BTCUSDT.P"}
		binanceSignal := trading.Signal{Action: trading.ActionShort, TargetExchange: trading.ExchangeBinance, Coinpair: "ETH", Ticker: "BINANCE:ETHUSDT.P"}
		if _, _, err := store.RecordAccepted(okxSignal, "okx", now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RecordAccepted(binanceSignal, "binance", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		okx := store.ListByTargetExchange(trading.ExchangeOKX, 10)
		if len(okx) != 1 || okx[0].TargetExchange != trading.ExchangeOKX || okx[0].Coinpair != "BTC" {
			t.Fatalf("bad OKX filtered orders: %#v", okx)
		}
		binance := store.ListByTargetExchange(trading.ExchangeBinance, 10)
		if len(binance) != 1 || binance[0].TargetExchange != trading.ExchangeBinance || binance[0].Coinpair != "ETH" {
			t.Fatalf("bad Binance filtered orders: %#v", binance)
		}
	}
	t.Run("memory", func(t *testing.T) {
		store, err := NewOrderStore("")
		if err != nil {
			t.Fatal(err)
		}
		run(t, store)
	})
	t.Run("sqlite", func(t *testing.T) {
		store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		run(t, store)
	})
}

func TestOrderStoreListPageAndCountMemoryAndSQLite(t *testing.T) {
	run := func(t *testing.T, store *OrderStore) {
		t.Helper()
		now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
		for i := 0; i < 5; i++ {
			exchange := trading.ExchangeBinance
			if i%2 == 0 {
				exchange = trading.ExchangeOKX
			}
			signal := trading.Signal{
				Action:         trading.ActionLong,
				TargetExchange: exchange,
				Coinpair:       "COIN" + strconv.Itoa(i),
				Ticker:         "COIN" + strconv.Itoa(i) + "USDT",
			}
			if _, _, err := store.RecordAccepted(signal, "page-"+strconv.Itoa(i), now.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		page := store.ListPage(2, 1)
		if len(page) != 2 || page[0].Coinpair != "COIN3" || page[1].Coinpair != "COIN2" {
			t.Fatalf("bad unfiltered page: %#v", page)
		}
		okxPage := store.ListByTargetExchangePage(trading.ExchangeOKX, 1, 1)
		if len(okxPage) != 1 || okxPage[0].Coinpair != "COIN2" {
			t.Fatalf("bad OKX page: %#v", okxPage)
		}
		if got := store.Count(); got != 5 {
			t.Fatalf("count = %d", got)
		}
		if got := store.CountByTargetExchange(trading.ExchangeOKX); got != 3 {
			t.Fatalf("OKX count = %d", got)
		}
	}
	t.Run("memory", func(t *testing.T) {
		store, err := NewOrderStore("")
		if err != nil {
			t.Fatal(err)
		}
		run(t, store)
	})
	t.Run("sqlite", func(t *testing.T) {
		store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		run(t, store)
	})
}

func TestOrderStoreSearchPageAndCountMemoryAndSQLite(t *testing.T) {
	run := func(t *testing.T, store *OrderStore) {
		t.Helper()
		now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
		btcSignal := trading.Signal{
			Action:         trading.ActionLong,
			APIID:          "main",
			Exchange:       "OKX",
			TargetExchange: trading.ExchangeOKX,
			Coinpair:       "BTCUSDT.P",
			Ticker:         "OKX:BTCUSDT.P",
			Price:          trading.NewFlexibleFloat(61000),
			Amount:         trading.NewFlexibleFloat(500),
		}
		btc, _, err := store.RecordAccepted(btcSignal, "search-btc", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSubmitted(btc.SignalID, trading.OrderResult{TargetExchange: trading.ExchangeOKX, InstID: "BTC-USDT-SWAP", ClOrdID: "client-btc", OrdID: "okx-999"}, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		ethSignal := trading.Signal{
			Action:         trading.ActionShort,
			APIID:          "binance-main",
			Exchange:       "BINANCE",
			TargetExchange: trading.ExchangeBinance,
			Coinpair:       "ETHUSDT.P",
			Ticker:         "BINANCE:ETHUSDT.P",
			Price:          trading.NewFlexibleFloat(3400),
			Amount:         trading.NewFlexibleFloat(750),
		}
		eth, _, err := store.RecordAccepted(ethSignal, "search-eth", now.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSubmitted(eth.SignalID, trading.OrderResult{TargetExchange: trading.ExchangeBinance, InstID: "ETHUSDT", ClOrdID: "client-eth", OrdID: "bn-321"}, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		solSignal := trading.Signal{
			Action:         trading.ActionLong,
			APIID:          "backup",
			Exchange:       "OKX",
			TargetExchange: trading.ExchangeOKX,
			Coinpair:       "SOLUSDT.P",
			Ticker:         "OKX:SOLUSDT.P",
			Amount:         trading.NewFlexibleFloat(250),
		}
		sol, _, err := store.RecordAccepted(solSignal, "search-sol", now.Add(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFailedCode(sol.SignalID, "51001", errors.New("Instrument ID does not exist"), now.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}

		if page := store.ListSearchPage("eth", 10, 0); len(page) != 1 || page[0].Coinpair != "ETHUSDT.P" {
			t.Fatalf("search by symbol failed: %#v", page)
		}
		if page := store.ListSearchPage("500", 10, 0); len(page) != 1 || page[0].Coinpair != "BTCUSDT.P" {
			t.Fatalf("search by amount failed: %#v", page)
		}
		if page := store.ListSearchPage("okx-999", 10, 0); len(page) != 1 || page[0].Coinpair != "BTCUSDT.P" {
			t.Fatalf("search by order id failed: %#v", page)
		}
		if page := store.ListSearchPage("okx instrument", 10, 0); len(page) != 1 || page[0].Coinpair != "SOLUSDT.P" {
			t.Fatalf("multi-key search should match error text and exchange: %#v", page)
		}
		if page := store.ListSearchByTargetExchangePage(trading.ExchangeOKX, "usdt.p", 10, 0); len(page) != 2 {
			t.Fatalf("exchange-scoped search count failed: %#v", page)
		}
		if page := store.ListSearchByTargetExchangePage(trading.ExchangeBinance, "btc", 10, 0); len(page) != 0 {
			t.Fatalf("exchange-scoped search should exclude OKX result: %#v", page)
		}
		if got := store.CountSearch("usdt.p"); got != 3 {
			t.Fatalf("search count = %d", got)
		}
		if got := store.CountSearchByTargetExchange(trading.ExchangeOKX, "usdt.p"); got != 2 {
			t.Fatalf("exchange search count = %d", got)
		}
		if page := store.ListSearchPage("missing", 10, 0); len(page) != 0 || store.CountSearch("missing") != 0 {
			t.Fatalf("missing search should be empty: page=%#v count=%d", page, store.CountSearch("missing"))
		}
		empty := store.ListSearchPage("   ", 2, 1)
		if len(empty) != 2 || empty[0].Coinpair != "ETHUSDT.P" || empty[1].Coinpair != "BTCUSDT.P" {
			t.Fatalf("empty search should preserve pagination: %#v", empty)
		}
	}
	t.Run("memory", func(t *testing.T) {
		store, err := NewOrderStore("")
		if err != nil {
			t.Fatal(err)
		}
		run(t, store)
	})
	t.Run("sqlite", func(t *testing.T) {
		store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		run(t, store)
	})
}

func TestOrderStoreFilteredPageAndCountMemoryAndSQLite(t *testing.T) {
	run := func(t *testing.T, store *OrderStore) {
		t.Helper()
		now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
		next := 0
		signal := func(coinpair, exchange string) trading.Signal {
			return trading.Signal{
				Action:         trading.ActionLong,
				TargetExchange: exchange,
				Coinpair:       coinpair,
				Ticker:         coinpair + "USDT.P",
			}
		}
		accepted, _, err := store.RecordAccepted(signal("ACCEPTED", trading.ExchangeOKX), "filter-accepted", now)
		if err != nil || accepted.Status != StatusAccepted {
			t.Fatalf("seed accepted: record=%#v err=%v", accepted, err)
		}
		next++
		submitted, _, err := store.RecordAccepted(signal("SUBMITTED", trading.ExchangeOKX), "filter-submitted", now.Add(time.Duration(next)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSubmitted(submitted.SignalID, trading.OrderResult{OrdID: "submitted-1"}, now.Add(time.Duration(next+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
		next += 2
		failed, _, err := store.RecordAccepted(signal("FAILED", trading.ExchangeOKX), "filter-failed", now.Add(time.Duration(next)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFailed(failed.SignalID, errors.New("seed failure"), now.Add(time.Duration(next+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
		next += 2
		if _, err := store.RecordRejected(signal("REJECTED", trading.ExchangeOKX), "invalid_signal", errors.New("seed rejection"), now.Add(time.Duration(next)*time.Second)); err != nil {
			t.Fatal(err)
		}
		next++
		if _, err := store.RecordIgnoredReason(signal("BTC-IGNORED", trading.ExchangeOKX), "adx_transition", "seed ignored", now.Add(time.Duration(next)*time.Second)); err != nil {
			t.Fatal(err)
		}
		next++
		if _, err := store.RecordIgnoredReason(signal("ETH-IGNORED", trading.ExchangeOKX), "coinpair_filtered", "seed ignored", now.Add(time.Duration(next)*time.Second)); err != nil {
			t.Fatal(err)
		}
		next++
		if _, err := store.RecordIgnoredReason(signal("BTC-IGNORED", trading.ExchangeBinance), "outside_market_top100", "seed ignored", now.Add(time.Duration(next)*time.Second)); err != nil {
			t.Fatal(err)
		}
		next++
		duplicateSignal := signal("DUPLICATE", trading.ExchangeOKX)
		if _, duplicate, err := store.RecordAccepted(duplicateSignal, "filter-duplicate", now.Add(time.Duration(next)*time.Second)); err != nil || duplicate {
			t.Fatalf("seed duplicate source: duplicate=%v err=%v", duplicate, err)
		}
		next++
		if record, duplicate, err := store.RecordAccepted(duplicateSignal, "filter-duplicate", now.Add(time.Duration(next)*time.Second)); err != nil || !duplicate || record.Status != StatusDuplicate {
			t.Fatalf("seed duplicate: record=%#v duplicate=%v err=%v", record, duplicate, err)
		}

		expectedCounts := map[OrderStatus]int{
			StatusAccepted:  2,
			StatusDuplicate: 1,
			StatusSubmitted: 1,
			StatusFailed:    1,
			StatusRejected:  1,
			StatusIgnored:   3,
		}
		for status, expected := range expectedCounts {
			page := store.ListFilteredPage("", status, "", 20, 0)
			if len(page) != expected || store.CountFiltered("", status, "") != expected {
				t.Fatalf("status %s: page=%#v count=%d expected=%d", status, page, store.CountFiltered("", status, ""), expected)
			}
			for _, record := range page {
				if record.Status != status {
					t.Fatalf("status %s returned %#v", status, record)
				}
			}
		}

		combined := store.ListFilteredPage(trading.ExchangeOKX, StatusIgnored, "btc", 10, 0)
		if len(combined) != 1 || combined[0].Coinpair != "BTC-IGNORED" || store.CountFiltered(trading.ExchangeOKX, StatusIgnored, "btc") != 1 {
			t.Fatalf("combined filter mismatch: %#v", combined)
		}
		ignoredPage := store.ListFilteredPage(trading.ExchangeOKX, StatusIgnored, "ignored", 1, 1)
		if len(ignoredPage) != 1 || ignoredPage[0].Status != StatusIgnored || store.CountFiltered(trading.ExchangeOKX, StatusIgnored, "ignored") != 2 {
			t.Fatalf("filtered pagination mismatch: %#v", ignoredPage)
		}
		if all := store.ListFilteredPage("", "", "", 20, 0); len(all) != store.Count() {
			t.Fatalf("empty filters should return all: page=%d total=%d", len(all), store.Count())
		}
	}

	t.Run("memory", func(t *testing.T) {
		store, err := NewOrderStore("")
		if err != nil {
			t.Fatal(err)
		}
		run(t, store)
	})
	t.Run("sqlite", func(t *testing.T) {
		store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		run(t, store)
	})
}

func TestSQLiteOrderStoreReadsLegacyRowsWithNullExchangeColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tvbot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE orders (
		signal_id TEXT PRIMARY KEY,
		dedupe_key TEXT NOT NULL,
		status TEXT NOT NULL,
		action TEXT,
		api_id TEXT,
		coinpair TEXT,
		ticker TEXT,
		price TEXT,
		leverage INTEGER,
		amount TEXT,
		token_hash TEXT,
		accepted_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		result_json TEXT,
		error_code TEXT,
		error TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	_, err = db.Exec(`INSERT INTO orders
		(signal_id, dedupe_key, status, action, api_id, coinpair, ticker, price, leverage, amount, token_hash, accepted_at, updated_at, result_json, error_code, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL)`,
		"sig-old",
		"dedupe-old",
		string(StatusAccepted),
		string(trading.ActionLong),
		"default",
		"BTC",
		"BTCUSDT",
		"50000",
		5,
		"100",
		"tokenhash",
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteOrderStore(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records := store.List(10)
	if len(records) != 1 || records[0].SignalID != "sig-old" || records[0].TargetExchange != trading.ExchangeOKX {
		t.Fatalf("legacy order should remain readable after exchange migration: %#v", records)
	}
	var sourceAction, sourceExchange, targetExchange, tradeEnv, rawJSON, marketStrategy, riskJSON, orderIntent, positionEffect, positionSide string
	var adx sql.NullFloat64
	if err := store.db.QueryRow(`SELECT source_action, source_exchange, target_exchange, trade_env, raw_json, adx, market_strategy, risk_json, order_intent, position_effect, position_side FROM orders WHERE signal_id = 'sig-old'`).Scan(&sourceAction, &sourceExchange, &targetExchange, &tradeEnv, &rawJSON, &adx, &marketStrategy, &riskJSON, &orderIntent, &positionEffect, &positionSide); err != nil {
		t.Fatal(err)
	}
	if sourceAction != "" || sourceExchange != "" || targetExchange != trading.ExchangeOKX || tradeEnv != trading.TradeEnvDemo || rawJSON != "" || adx.Valid || marketStrategy != "" || riskJSON != "" || orderIntent != "" || positionEffect != "" || positionSide != "" {
		t.Fatalf("legacy order columns not backfilled source_action=%q source=%q target=%q trade_env=%q raw_json=%q adx=%#v market_strategy=%q risk_json=%q order_intent=%q position_effect=%q position_side=%q", sourceAction, sourceExchange, targetExchange, tradeEnv, rawJSON, adx, marketStrategy, riskJSON, orderIntent, positionEffect, positionSide)
	}
}

func TestSQLiteSymbolCatalogCache(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if err := store.UpsertSymbolCatalogCaches([]SymbolCatalogCache{{
		Exchange:    trading.ExchangeBinance,
		Env:         trading.TradeEnvLive,
		PayloadJSON: `{"env":"live","count":1,"instruments":[{"symbol":"BTCUSDT"}]}`,
		Count:       1,
		SyncedAt:    now,
		AttemptedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListSymbolCatalogCaches()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Exchange != trading.ExchangeBinance || items[0].Env != trading.TradeEnvLive || items[0].Count != 1 || items[0].SyncedAt.IsZero() {
		t.Fatalf("bad symbol cache item: %#v", items)
	}
	if err := store.UpsertSymbolCatalogCaches([]SymbolCatalogCache{{
		Exchange:    trading.ExchangeBinance,
		Env:         trading.TradeEnvLive,
		PayloadJSON: `{"env":"live","count":0,"instruments":[],"error":"unavailable"}`,
		Count:       0,
		AttemptedAt: now.Add(time.Hour),
		Error:       "unavailable",
	}}); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListSymbolCatalogCaches()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Count != 0 || items[0].Error != "unavailable" || !items[0].SyncedAt.IsZero() {
		t.Fatalf("symbol cache upsert should replace metadata: %#v", items)
	}
}

func TestSQLiteAnalysisTables(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if err := store.UpsertMarketCandles([]MarketCandle{{
		InstID: "USDT-USD",
		Bar:    "1H",
		TS:     now.UnixMilli(),
		Open:   "0.9990",
		Close:  "0.9991",
	}}, now); err != nil {
		t.Fatal(err)
	}
	candles, err := store.ListMarketCandles("USDT-USD", "1H", now.Add(-time.Hour), 72)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Close != "0.9991" {
		t.Fatalf("bad candles: %#v", candles)
	}
	fill := OKXFill{
		APIID:    "default",
		InstType: "SWAP",
		InstID:   "BTC-USDT-SWAP",
		TradeID:  "trade-1",
		FillPnl:  "2.5",
		Fee:      "-0.1",
		FeeCcy:   "USDT",
		FillTime: now.UnixMilli(),
	}
	if err := store.UpsertOKXFills([]OKXFill{fill, fill}, now); err != nil {
		t.Fatal(err)
	}
	fills, err := store.ListOKXFills("default", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1 || fills[0].TradeID != "trade-1" {
		t.Fatalf("bad fills: %#v", fills)
	}
	if err := store.UpsertUSDTBalanceSnapshot(USDTBalanceSnapshot{
		APIID:            "default",
		Env:              "demo",
		BucketTS:         now.UnixMilli(),
		ObservedAt:       now,
		TotalEq:          "1001",
		Eq:               "1000",
		EqUsd:            "999.5",
		AvailEq:          "900",
		AvailBal:         "899",
		CashBal:          "950",
		FrozenBal:        "50",
		BalanceUpdatedAt: now.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertUSDTBalanceSnapshot(USDTBalanceSnapshot{
		APIID:      "default",
		Env:        "demo",
		BucketTS:   now.UnixMilli(),
		ObservedAt: now.Add(10 * time.Minute),
		Eq:         "1002",
		EqUsd:      "1001.5",
	}); err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.ListUSDTBalanceSnapshots("default", "demo", now.Add(-time.Hour), 72)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].EqUsd != "1001.5" || !snapshots[0].ObservedAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("bad USDT balance snapshots: %#v", snapshots)
	}
	minuteObservedAt := now.Add(14*time.Minute + 45*time.Second)
	if err := store.UpsertUSDTBalanceSnapshot(USDTBalanceSnapshot{
		APIID:      "default",
		Env:        "demo",
		ObservedAt: minuteObservedAt,
		Eq:         "1003",
		EqUsd:      "1002.5",
	}); err != nil {
		t.Fatal(err)
	}
	snapshots, err = store.ListUSDTBalanceSnapshots("default", "demo", now.Add(-time.Hour), 72)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[1].BucketTS != minuteObservedAt.UTC().Truncate(time.Minute).UnixMilli() || snapshots[1].EqUsd != "1002.5" {
		t.Fatalf("USDT balance snapshots should bucket by minute: %#v", snapshots)
	}
	if err := store.UpsertUSDTBalanceSnapshot(USDTBalanceSnapshot{
		Exchange:   "binance",
		APIID:      "default",
		Env:        "demo",
		BucketTS:   now.UnixMilli(),
		ObservedAt: now,
		Eq:         "2000",
		EqUsd:      "2000",
	}); err != nil {
		t.Fatal(err)
	}
	okxSnapshots, err := store.ListUSDTBalanceSnapshots("default", "demo", now.Add(-time.Hour), 72)
	if err != nil {
		t.Fatal(err)
	}
	binanceSnapshots, err := store.ListExchangeUSDTBalanceSnapshots("binance", "default", "demo", now.Add(-time.Hour), 72)
	if err != nil {
		t.Fatal(err)
	}
	if len(okxSnapshots) != 2 || okxSnapshots[0].Exchange != "okx" || len(binanceSnapshots) != 1 || binanceSnapshots[0].EqUsd != "2000" {
		t.Fatalf("exchange-specific USDT snapshots not isolated okx=%#v binance=%#v", okxSnapshots, binanceSnapshots)
	}
}
