package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func openAlertStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, context.Background()
}

func alertRow(id, symbol, side string, at time.Time, status string) domain.LiquidationAlert {
	return domain.LiquidationAlert{ID: id, Symbol: symbol, Side: side, TradeSide: side, WindowMinutes: 3, NotionalUSD: 400000, PeakUSD: 420000, ThresholdUSD: 300000, EventCount: 4, WindowStart: at.Add(-2 * time.Minute), TriggeredAt: at, QuietSeconds: 600, QuietUntil: at.Add(10 * time.Minute), Status: status, UpdatedAt: at}
}

func signalRow(id, symbol, kind, side string, at time.Time) domain.TradeSignal {
	return domain.TradeSignal{ID: id, AlertID: "alert-" + symbol, Symbol: symbol, Kind: kind, Side: side, LiquidationSide: side, CreatedAt: at, AlertNotionalUSD: 400000, ThresholdUSD: 300000, WindowMinutes: 3}
}

// TestLiquidationAlertRoundTripAndPagination covers the alert history page, including
// the novelty flag that gates the first notification.
func TestLiquidationAlertRoundTripAndPagination(t *testing.T) {
	store, ctx := openAlertStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		side := "long"
		if i%2 == 1 {
			side = "short"
		}
		created, err := store.SaveLiquidationAlert(ctx, alertRow("a"+string(rune('a'+i)), "BTCUSDT", side, at, domain.AlertStateWatching))
		if err != nil {
			t.Fatal(err)
		}
		if !created {
			t.Fatalf("row %d must be new", i)
		}
	}
	// Re-saving the same ID refreshes the row and reports it as not new, which is how
	// quiet-period updates avoid re-notifying.
	updated, err := store.SaveLiquidationAlert(ctx, alertRow("aa", "BTCUSDT", "long", base, domain.AlertStateAggressive))
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("an existing alert must not report as new")
	}
	page, err := store.LiquidationAlerts(ctx, "", "", 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 3 || page.AlertCount != 5 || page.NextCursor == "" {
		t.Fatalf("unexpected first page: %d rows total %d cursor %q", len(page.Rows), page.AlertCount, page.NextCursor)
	}
	if !page.Rows[0].TriggeredAt.After(page.Rows[2].TriggeredAt) {
		t.Fatalf("rows must be newest first: %+v", page.Rows)
	}
	if page.Rows[0].Status != domain.AlertStateWatching || page.Rows[0].Symbol != "BTCUSDT" || page.Rows[0].QuietSeconds != 600 {
		t.Fatalf("the payload must round trip: %+v", page.Rows[0])
	}
	recent, err := store.LiquidationAlerts(ctx, "ALL", "long", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	var refreshed bool
	for _, row := range recent.Rows {
		if row.ID == "aa" {
			refreshed = row.Status == domain.AlertStateAggressive
		}
	}
	if !refreshed {
		t.Fatalf("the updated status must round trip: %+v", recent.Rows)
	}
	next, err := store.LiquidationAlerts(ctx, "", "", 3, page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Rows) != 2 || next.NextCursor != "" {
		t.Fatalf("unexpected second page: %d rows cursor %q", len(next.Rows), next.NextCursor)
	}
	short, err := store.LiquidationAlerts(ctx, "BTCUSDT", "short", 10, "")
	if err != nil || len(short.Rows) != 2 || short.AlertCount != 2 {
		t.Fatalf("the side filter failed: %d rows count %d err %v", len(short.Rows), short.AlertCount, err)
	}
	none, err := store.LiquidationAlerts(ctx, "ETHUSDT", "", 10, "")
	if err != nil || len(none.Rows) != 0 || none.AlertCount != 0 {
		t.Fatalf("the symbol filter failed: %+v err %v", none, err)
	}
	if _, err = store.LiquidationAlerts(ctx, "", "", 0, ""); err == nil {
		t.Fatal("an invalid limit must be rejected")
	}
	if _, err = store.LiquidationAlerts(ctx, "", "", 10, "not-a-cursor"); err == nil {
		t.Fatal("an invalid cursor must be rejected")
	}
	if _, err = store.SaveLiquidationAlert(ctx, domain.LiquidationAlert{Symbol: "BTCUSDT"}); err == nil {
		t.Fatal("an alert without an id must be rejected")
	}
	oldest, err := store.AlertOldestTime(ctx)
	if err != nil || !oldest.Equal(base) {
		t.Fatalf("oldest alert: %v err %v", oldest, err)
	}
}

// TestTradeSignalNoveltyAndNotification covers the dedupe path that keeps a restart
// replay from notifying twice, plus the kind counters the page shows.
func TestTradeSignalNoveltyAndNotification(t *testing.T) {
	store, ctx := openAlertStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	created, err := store.SaveTradeSignal(ctx, signalRow("s1", "BTCUSDT", domain.SignalAggressive, "long", base))
	if err != nil || !created {
		t.Fatalf("the first insert must be new: %v %v", created, err)
	}
	created, err = store.SaveTradeSignal(ctx, signalRow("s1", "BTCUSDT", domain.SignalAggressive, "long", base))
	if err != nil || created {
		t.Fatalf("a replayed signal must not report as new: %v %v", created, err)
	}
	if _, err = store.SaveTradeSignal(ctx, signalRow("s2", "BTCUSDT", domain.SignalSteady, "long", base.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveTradeSignal(ctx, signalRow("s3", "ETHUSDT", domain.SignalAggressive, "short", base.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	total, aggressive, steady, err := store.SignalTotals(ctx)
	if err != nil || total != 3 || aggressive != 2 || steady != 1 {
		t.Fatalf("totals wrong: %d/%d/%d err %v", total, aggressive, steady, err)
	}
	page, err := store.TradeSignals(ctx, "", domain.SignalAggressive, 10, "")
	if err != nil || len(page.Rows) != 2 || page.SignalCount != 2 || page.AggressiveCount != 2 || page.SteadyCount != 0 {
		t.Fatalf("the kind filter failed: %+v err %v", page, err)
	}
	if err = store.MarkTradeSignalNotified(ctx, "s1", "queued", ""); err != nil {
		t.Fatal(err)
	}
	marked, err := store.TradeSignals(ctx, "BTCUSDT", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range marked.Rows {
		if row.ID != "s1" {
			continue
		}
		found = true
		if row.NotifyStatus != "queued" || row.NotifiedAt.IsZero() {
			t.Fatalf("the notification state must persist: %+v", row)
		}
	}
	if !found {
		t.Fatal("the marked signal is missing")
	}
	if err = store.MarkTradeSignalNotified(ctx, "missing", "queued", ""); err == nil {
		t.Fatal("marking an unknown signal must fail")
	}
	if _, err = store.TradeSignals(ctx, "", "", 500, ""); err == nil {
		t.Fatal("an oversized page must be rejected")
	}
	if _, err = store.SaveTradeSignal(ctx, domain.TradeSignal{Kind: domain.SignalAggressive}); err == nil {
		t.Fatal("a signal without an id must be rejected")
	}
}

// TestAlertTimelinePaginationAndFilters proves that alerts and both signal kinds
// share one stable chronological stream without dropping rows at page boundaries.
func TestAlertTimelinePaginationAndFilters(t *testing.T) {
	store, ctx := openAlertStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 12; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		symbol := "BTCUSDT"
		if i == 0 {
			symbol = "ETHUSDT"
		}
		if _, err := store.SaveLiquidationAlert(ctx, alertRow("timeline-a"+string(rune('a'+i)), symbol, "long", at, domain.AlertStateWatching)); err != nil {
			t.Fatal(err)
		}
		kind := domain.SignalAggressive
		if i%2 == 1 {
			kind = domain.SignalSteady
		}
		if _, err := store.SaveTradeSignal(ctx, signalRow("timeline-s"+string(rune('a'+i)), symbol, kind, "long", at.Add(30*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.AlertTimeline(ctx, "", "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 20 || first.TotalCount != 24 || first.AlertCount != 12 || first.AggressiveCount != 6 || first.SteadyCount != 6 || first.NextCursor == "" {
		t.Fatalf("unexpected first timeline page: %+v", first)
	}
	for i := 1; i < len(first.Rows); i++ {
		if first.Rows[i-1].EventAt.Before(first.Rows[i].EventAt) {
			t.Fatalf("timeline must be newest first: %+v", first.Rows)
		}
	}
	second, err := store.AlertTimeline(ctx, "", "", 20, first.NextCursor)
	if err != nil || len(second.Rows) != 4 || second.NextCursor != "" {
		t.Fatalf("unexpected second timeline page: %+v err %v", second, err)
	}
	steady, err := store.AlertTimeline(ctx, "BTCUSDT", domain.SignalSteady, 20, "")
	if err != nil || len(steady.Rows) != 6 || steady.TotalCount != 6 || steady.AlertCount != 11 || steady.AggressiveCount != 5 || steady.SteadyCount != 6 {
		t.Fatalf("timeline filters or counters are wrong: %+v err %v", steady, err)
	}
	for _, row := range steady.Rows {
		if row.Type != domain.SignalSteady || row.Signal == nil || row.Alert != nil || row.Signal.Symbol != "BTCUSDT" {
			t.Fatalf("unexpected filtered timeline row: %+v", row)
		}
	}
	if _, err = store.AlertTimeline(ctx, "", "unknown", 20, ""); err == nil {
		t.Fatal("an invalid timeline kind must be rejected")
	}
}

// TestWatchListAndSettingsPersistence covers the two small stores the alert page
// manages: the top volume watch list and the Telegram settings row.
func TestWatchListAndSettingsPersistence(t *testing.T) {
	store, ctx := openAlertStore(t)
	if _, found, err := store.AlertSettings(ctx); err != nil || found {
		t.Fatalf("a fresh database must report no stored settings: %v %v", found, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.ReplaceWatchSymbols(ctx, []domain.WatchSymbol{
		{Symbol: "BTCUSDT", Rank: 1, QuoteVolumeUSD: 9e9, UpdatedAt: now},
		{Symbol: "ETHUSDT", Rank: 2, QuoteVolumeUSD: 4e9, UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.WatchSymbols(ctx)
	if err != nil || len(rows) != 2 || rows[0].Symbol != "BTCUSDT" || rows[1].Rank != 2 || !rows[0].UpdatedAt.Equal(now) {
		t.Fatalf("the watch list must come back ranked: %+v err %v", rows, err)
	}
	// A refresh replaces the list instead of appending to it.
	if err := store.ReplaceWatchSymbols(ctx, []domain.WatchSymbol{{Symbol: "SOLUSDT", Rank: 1, QuoteVolumeUSD: 1e9, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	rows, err = store.WatchSymbols(ctx)
	if err != nil || len(rows) != 1 || rows[0].Symbol != "SOLUSDT" {
		t.Fatalf("the watch list must be replaced: %+v err %v", rows, err)
	}
	settings := domain.NotificationSettings{TelegramEnabled: true, TelegramToken: "123:secret", TelegramChatID: "@alerts", NotifyAlerts: false, NotifyAggressive: true, NotifySteady: false}
	if err := store.SaveAlertSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.AlertSettings(ctx)
	if err != nil || !found {
		t.Fatalf("the settings must be stored: %v %v", found, err)
	}
	if !stored.TelegramEnabled || stored.TelegramToken != "123:secret" || stored.TelegramChatID != "@alerts" || stored.NotifyAlerts || !stored.NotifyAggressive || stored.NotifySteady {
		t.Fatalf("the settings did not round trip: %+v", stored)
	}
	settings.TelegramEnabled = false
	if err := store.SaveAlertSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	stored, _, err = store.AlertSettings(ctx)
	if err != nil || stored.TelegramEnabled || stored.TelegramToken != "123:secret" {
		t.Fatalf("the settings row must be updated in place: %+v err %v", stored, err)
	}
}

// TestRecentLiquidationEventsAndAlertPruning covers the restart warm-up read and the
// retention horizon that keeps alerts and signals for backtesting.
func TestRecentLiquidationEventsAndAlertPruning(t *testing.T) {
	store, ctx := openAlertStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 4; i++ {
		event := domain.LiquidationEvent{ID: "e" + string(rune('a'+i)), Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: base.Add(time.Duration(i) * time.Minute), ReceivedAt: base, Price: 100, Quantity: 1, NotionalUSD: 1000, Coverage: "sampled"}
		if err := store.InsertLiquidation(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.InsertLiquidation(ctx, domain.LiquidationEvent{ID: "x", Exchange: "other", Symbol: "BTCUSDT", PositionSide: "long", EventTime: base.Add(3 * time.Minute), NotionalUSD: 1000}); err != nil {
		t.Fatal(err)
	}
	events, err := store.RecentLiquidationEvents(ctx, base.Add(30*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("only the three newer Binance events belong to the warm-up: %+v", events)
	}
	for i := 1; i < len(events); i++ {
		if events[i-1].EventTime.After(events[i].EventTime) {
			t.Fatalf("warm-up events must be oldest first: %+v", events)
		}
	}
	if _, err = store.RecentLiquidationEvents(ctx, base, 0); err == nil {
		t.Fatal("an invalid limit must be rejected")
	}
	for i := 0; i < 3; i++ {
		at := base.AddDate(0, 0, -200+i)
		if _, err = store.SaveLiquidationAlert(ctx, alertRow("old"+string(rune('a'+i)), "BTCUSDT", "long", at, domain.AlertStateExpired)); err != nil {
			t.Fatal(err)
		}
		if _, err = store.SaveTradeSignal(ctx, signalRow("olds"+string(rune('a'+i)), "BTCUSDT", domain.SignalAggressive, "long", at)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.SaveLiquidationAlert(ctx, alertRow("fresh", "BTCUSDT", "long", base, domain.AlertStateWatching)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveTradeSignal(ctx, signalRow("freshs", "BTCUSDT", domain.SignalSteady, "long", base)); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneAlerts(ctx, base.AddDate(0, 0, -domain.AlertRetentionDays)); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.LiquidationAlerts(ctx, "", "", 50, "")
	if err != nil || remaining.AlertCount != 1 || remaining.Rows[0].ID != "fresh" {
		t.Fatalf("expired alerts must be pruned: %+v err %v", remaining, err)
	}
	signals, err := store.TradeSignals(ctx, "", "", 50, "")
	if err != nil || signals.SignalCount != 1 || signals.Rows[0].ID != "freshs" {
		t.Fatalf("expired signals must be pruned: %+v err %v", signals, err)
	}
}
