package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

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
