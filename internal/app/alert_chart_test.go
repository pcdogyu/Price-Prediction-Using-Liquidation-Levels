package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestRenderAlertChartPNG(t *testing.T) {
	base := time.Date(2026, 10, 16, 3, 50, 0, 0, time.UTC)
	pngBytes, err := renderAlertChartPNG([]domain.MarketCandle{
		{Time: base, Open: 100, High: 104, Low: 99, Close: 103, VolumeUSD: 1000},
		{Time: base.Add(time.Minute), Open: 103, High: 105, Low: 101, Close: 102, VolumeUSD: 2000},
		{Time: base.Add(2 * time.Minute), Open: 102, High: 106, Low: 100, Close: 105, VolumeUSD: 500},
	}, base.Add(90*time.Second), 103, "long", "BTCUSDT", []domain.AlertTimelineItem{
		{Type: "alert", EventAt: base, Alert: &domain.LiquidationAlert{Symbol: "BTCUSDT", Side: "long", ReferencePrice: 103}},
		{Type: domain.SignalAggressive, EventAt: base.Add(time.Minute), Signal: &domain.TradeSignal{Symbol: "BTCUSDT", Side: "long", Kind: domain.SignalAggressive, ReferencePrice: 102}},
		{Type: domain.SignalSteady, EventAt: base.Add(2 * time.Minute), Signal: &domain.TradeSignal{Symbol: "BTCUSDT", Side: "short", Kind: domain.SignalSteady, ReferencePrice: 105}},
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("generated chart is not a PNG: %v", err)
	}
	if got := img.Bounds().Size(); got.X != 1280 || got.Y != 720 {
		t.Fatalf("chart size = %v, want 1280x720", got)
	}
}

func TestAlertChartUsesUTCPlus8AndSupportsZSymbols(t *testing.T) {
	at := time.Date(2026, 10, 16, 3, 50, 0, 0, time.UTC)
	if got := at.In(alertChartLocation).Format("2006-01-02 15:04:05 UTC+8"); got != "2026-10-16 11:50:00 UTC+8" {
		t.Fatalf("chart time = %q, want UTC+8 rendering", got)
	}
	if got := at.In(alertChartLocation).Format("15:04"); got != "11:50" {
		t.Fatalf("chart x-axis time = %q, want time without date", got)
	}
	if _, ok := chartGlyphs['Z']; !ok {
		t.Fatal("chart glyphs must include Z so ZECUSDT is not rendered as ?")
	}
	if _, ok := chartGlyphs['!']; !ok {
		t.Fatal("chart glyphs must include ! for the warning legend")
	}
	if _, ok := chartGlyphs['+']; !ok {
		t.Fatal("chart glyphs must include + for the UTC+8 title")
	}
}

func TestCurrentAlertChartTaskUsesLatestKlines(t *testing.T) {
	originalFetch := fetchAlertKlines
	t.Cleanup(func() { fetchAlertKlines = originalFetch })
	var gotUntil time.Time
	fetchAlertKlines = func(_ context.Context, _ string, _ string, _ int, until time.Time) ([]domain.MarketCandle, bool, error) {
		gotUntil = until
		base := time.Now().UTC().Truncate(time.Minute)
		return []domain.MarketCandle{{Time: base, Open: 100, High: 101, Low: 99, Close: 100.5, VolumeUSD: 10}}, false, nil
	}
	rtm := &alertRuntime{service: &Service{klineCache: make(map[string]klineCacheEntry), log: slog.Default()}}
	task := rtm.currentAlertChartTask("BTCUSDT", "long")
	if !strings.Contains(task.PhotoCaption, "触发报价时间") {
		t.Fatalf("manual chart caption = %q", task.PhotoCaption)
	}
	if _, err := task.Photo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gotUntil.IsZero() {
		t.Fatalf("manual chart must request the newest window, until=%v", gotUntil)
	}
}

func TestDrawTriangleDirection(t *testing.T) {
	up := image.NewRGBA(image.Rect(0, 0, 32, 32))
	down := image.NewRGBA(image.Rect(0, 0, 32, 32))
	marker := color.RGBA{R: 14, G: 132, B: 92, A: 255}
	drawTriangle(up, 16, 16, 4, marker, true, true)
	drawTriangle(down, 16, 16, 4, marker, true, false)
	if up.RGBAAt(12, 16) != marker || up.RGBAAt(12, 12) == marker {
		t.Fatal("up triangle must point upward")
	}
	if down.RGBAAt(12, 12) != marker || down.RGBAAt(12, 16) == marker {
		t.Fatal("down triangle must point downward")
	}
}
