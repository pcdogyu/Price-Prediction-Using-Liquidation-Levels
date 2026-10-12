package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestLiquidationSixPeriodBoundariesAndCursorIndependence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "periods.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	ages := []time.Duration{time.Millisecond, 5 * time.Minute, 5*time.Minute + time.Millisecond,
		15 * time.Minute, 15*time.Minute + time.Millisecond, time.Hour,
		time.Hour + time.Millisecond, 4 * time.Hour, 4*time.Hour + time.Millisecond,
		12 * time.Hour, 12*time.Hour + time.Millisecond, 24 * time.Hour,
		24*time.Hour + time.Millisecond, -time.Millisecond}
	for i, age := range ages {
		side, amount := "long", 100.
		if i%2 == 1 {
			side, amount = "short", 50
		}
		event := domain.LiquidationEvent{ID: fmt.Sprintf("eth-%d", i), Exchange: "binance", Symbol: "ETHUSDT", PositionSide: side, EventTime: at.Add(-age), ReceivedAt: at, Price: 100, Quantity: 1, NotionalUSD: amount, Coverage: "sampled"}
		if err = s.InsertLiquidation(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []struct{ exchange, symbol string }{{"binance", "BTCUSDT"}, {"okx", "ETHUSDT"}} {
		if err = s.InsertLiquidation(ctx, domain.LiquidationEvent{ID: source.exchange + "-other", Exchange: source.exchange, Symbol: source.symbol, PositionSide: "short", EventTime: at.Add(-time.Millisecond), ReceivedAt: at, Price: 100, Quantity: 1, NotionalUSD: 99999, Coverage: "sampled"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.InsertLiquidation(ctx, domain.LiquidationEvent{ID: "exact-cutoff", Exchange: "binance", Symbol: "ETHUSDT", PositionSide: "long", EventTime: at, ReceivedAt: at, Price: 100, Quantity: 1, NotionalUSD: 99999, Coverage: "sampled"}); err != nil {
		t.Fatal(err)
	}
	f := domain.LiquidationFilter{Symbol: "ETHUSDT", From: at.Add(-30 * time.Hour), To: at, Limit: 1}
	for page := 0; page < 2; page++ {
		result, err := s.LiquidationHistory(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		labels := []string{"5M", "15M", "1H", "4H", "12H", "24H"}
		if len(result.Periods) != len(labels) {
			t.Fatalf("periods=%+v", result.Periods)
		}
		for i, p := range result.Periods {
			if p.Label != labels[i] || p.Count != 2*(i+1) || p.LongUSD != float64(100*(i+1)) || p.ShortUSD != float64(50*(i+1)) {
				t.Fatalf("page=%d period=%+v", page, p)
			}
		}
		f.Cursor = result.NextCursor
		if f.Cursor == "" {
			t.Fatal("expected a second detail page")
		}
	}
	// An explicit start time still bounds every period, including the new windows.
	f.Cursor, f.From = "", at.Add(-10*time.Minute)
	result, err := s.LiquidationHistory(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range result.Periods {
		if i == 0 {
			continue
		}
		if p.Count != 3 || p.LongUSD != 200 || p.ShortUSD != 50 {
			t.Fatalf("bounded period=%+v", p)
		}
	}
}

func TestLiquidationCursorStableUnderNewEvents(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"a", "b", "c"} {
		if err = s.InsertLiquidation(ctx, domain.LiquidationEvent{ID: id, Exchange: "binance", Symbol: "SOLUSDT", PositionSide: "long", EventTime: at, ReceivedAt: at, Price: 10, Quantity: 1, NotionalUSD: 10, Coverage: "sampled"}); err != nil {
			t.Fatal(err)
		}
	}
	f := domain.LiquidationFilter{Symbol: "ALL", From: at.Add(-time.Hour), To: at.Add(time.Hour), Limit: 2}
	p, err := s.LiquidationHistory(ctx, f)
	if err != nil || len(p.Rows) != 2 || p.Rows[0].ID != "c" || p.NextCursor == "" {
		t.Fatalf("page=%+v error=%v", p, err)
	}
	if err = s.InsertLiquidation(ctx, domain.LiquidationEvent{ID: "new", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "short", EventTime: at.Add(time.Minute), ReceivedAt: at, Price: 1, Quantity: 1, NotionalUSD: 1, Coverage: "sampled"}); err != nil {
		t.Fatal(err)
	}
	f.Cursor = p.NextCursor
	p, err = s.LiquidationHistory(ctx, f)
	if err != nil || len(p.Rows) != 1 || p.Rows[0].ID != "a" {
		t.Fatalf("page=%+v error=%v", p, err)
	}
	f.Cursor = ""
	f.Field = "quantity"
	f.Minimum = 2
	p, err = s.LiquidationHistory(ctx, f)
	if err != nil || len(p.Rows) != 0 {
		t.Fatal(p, err)
	}
}
func TestDashboardPersistenceMergeRestartAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	zero := 0.
	ratio := 2.
	if err = s.SaveMarketMetric(ctx, domain.MarketMetric{Symbol: "ETHUSDT", Time: now, FundingRate: &zero}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveMarketMetric(ctx, domain.MarketMetric{Symbol: "ETHUSDT", Time: now, TopPositionRatio: &ratio}); err != nil {
		t.Fatal(err)
	}
	for _, age := range []int{0, 31} {
		b := domain.BookSnapshot{Symbol: "ETHUSDT", Time: now.AddDate(0, 0, -age), BucketWidth: .1, Bids: []domain.DepthLevel{{Price: 100, Quantity: 2, NotionalUSD: 200}}, Asks: []domain.DepthLevel{}}
		if err = s.SaveBookSnapshot(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SaveWallEvent(ctx, domain.WallEvent{ID: "active", Symbol: "ETHUSDT", QualifiedAt: now, StartedAt: now, LastSeen: now, PeakUSD: 300000}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.InterruptWallEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.PruneDashboard(ctx, now); err != nil {
		t.Fatal(err)
	}
	h, err := s.WallHistory(ctx, "ETHUSDT", "snapshots", now.AddDate(0, 0, -180), now.Add(time.Second), 50, "")
	if err != nil || len(h.Snapshots) != 1 || h.Snapshots[0].Bids[0].NotionalUSD != 200 {
		t.Fatal(h, err)
	}
	h, err = s.WallHistory(ctx, "ETHUSDT", "events", now.Add(-time.Hour), now.Add(time.Second), 50, "")
	if err != nil || len(h.Events) != 1 || h.Events[0].EndReason != "restart" || h.Events[0].EndedAt == nil {
		t.Fatal(h, err)
	}
	ms, err := s.MarketMetrics(ctx, "ETHUSDT", now.Add(-time.Hour), now.Add(time.Second))
	if err != nil || len(ms) != 1 || ms[0].FundingRate == nil || *ms[0].FundingRate != 0 || ms[0].TopPositionRatio == nil {
		t.Fatal(ms, err)
	}
}
