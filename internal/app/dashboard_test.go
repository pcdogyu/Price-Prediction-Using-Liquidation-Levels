package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
)

func testBook(at time.Time) domain.BookSnapshot {
	return domain.BookSnapshot{Symbol: "ETHUSDT", Time: at, BucketWidth: 1, BestBid: 100, BestAsk: 102, Bids: []domain.DepthLevel{{Price: 100, NotionalUSD: 400000}, {Price: 99, NotionalUSD: 350000}}, Asks: []domain.DepthLevel{{Price: 102, NotionalUSD: 1000}}}
}
func TestWallQualificationDisappearanceAndInterruption(t *testing.T) {
	tracks := map[string]*wallTrack{}
	at := time.Now().UTC()
	var events []domain.WallEvent
	for i := 0; i <= 12; i++ {
		changed, _ := updateWalls(tracks, testBook(at.Add(time.Duration(i)*250*time.Millisecond)))
		events = append(events, changed...)
	}
	if len(events) != 2 || events[0].DurationMS != 3000 {
		t.Fatal(events)
	}
	b := testBook(at.Add(19 * time.Second))
	b.Bids = nil
	ended, _ := updateWalls(tracks, b)
	if len(ended) != 2 || ended[0].EndReason != "disappeared" {
		t.Fatal(ended)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	svc := New(config.Config{}, s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	for i := 0; i <= 12; i++ {
		if err = svc.recordBook(ctx, testBook(at.Add(time.Duration(i)*250*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}
	svc.interruptStaleWalls(ctx, at.Add(9*time.Second))
	history, err := s.WallHistory(ctx, "ETHUSDT", "events", at.Add(-time.Second), at.Add(time.Minute), 50, "")
	if err != nil || len(history.Events) != 2 || history.Events[0].EndReason != "disconnected" || history.Events[0].DurationMS != 3000 {
		t.Fatal(history, err)
	}
}
func TestShortWallsDoNotAccumulateAcrossGaps(t *testing.T) {
	tracks := map[string]*wallTrack{}
	at := time.Now().UTC()
	for i := 0; i < 8; i++ {
		updateWalls(tracks, testBook(at.Add(time.Duration(i)*250*time.Millisecond)))
	}
	b := testBook(at.Add(2 * time.Second))
	b.Bids[0].NotionalUSD = 100
	updateWalls(tracks, b)
	for i := 0; i < 8; i++ {
		events, _ := updateWalls(tracks, testBook(at.Add(3*time.Second+time.Duration(i)*250*time.Millisecond)))
		for _, e := range events {
			if e.Price == 100 {
				t.Fatal("noncontinuous wall qualified")
			}
		}
	}
}
func TestMarketCVDUsesWindowBaselineAndPreservesGaps(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Minute)
	cs := []domain.Candle{}
	for i := 0; i < 10; i++ {
		cs = append(cs, domain.Candle{Time: at.Add(time.Duration(i) * time.Minute), VolumeUSD: 100, TakerBuyUSD: 60})
	}
	series := BuildMarketSeries(cs, nil, at, at.Add(10*time.Minute), 5*time.Minute)
	if len(series) != 2 || series[0].CVDUSD == nil || *series[0].CVDUSD != 100 || *series[1].CVDUSD != 200 {
		t.Fatal(series)
	}
	series = BuildMarketSeries(cs, nil, at.Add(5*time.Minute), at.Add(10*time.Minute), 5*time.Minute)
	if *series[0].CVDUSD != 100 {
		t.Fatal("CVD did not reset at selected window")
	}
	cs = append(cs[:2], cs[3:]...)
	series = BuildMarketSeries(cs, nil, at, at.Add(10*time.Minute), 5*time.Minute)
	if series[0].CVDUSD != nil || series[1].CVDUSD != nil || series[1].DeltaUSD == nil {
		t.Fatal("gap became a valid cumulative value")
	}
}
