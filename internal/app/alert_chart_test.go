package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/engine"
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
	}, &engine.MapResult{MarkPrice: 103, TopLong: []domain.LiquidationPeak{{Price: 100, AmountUSD: 300}, {Price: 101, AmountUSD: 200}, {Price: 102, AmountUSD: 100}}, TopShort: []domain.LiquidationPeak{{Price: 106, AmountUSD: 300}, {Price: 107, AmountUSD: 200}, {Price: 108, AmountUSD: 100}}})
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

func TestRenderAlertChartPNGDrawsCoinGlassTopThreeLines(t *testing.T) {
	base := time.Date(2026, 10, 16, 3, 50, 0, 0, time.UTC)
	candles := []domain.MarketCandle{
		{Time: base, Open: 100, High: 104, Low: 99, Close: 103, VolumeUSD: 1000},
		{Time: base.Add(time.Minute), Open: 103, High: 105, Low: 101, Close: 102, VolumeUSD: 2000},
	}
	levels := &engine.MapResult{
		MarkPrice: 103,
		TopLong:   []domain.LiquidationPeak{{Price: 100}, {Price: 101}, {Price: 102}},
		TopShort:  []domain.LiquidationPeak{{Price: 104.5}, {Price: 105.5}, {Price: 106.5}},
	}
	pngBytes, err := renderAlertChartPNG(candles, base, 103, "long", "BTCUSDT", nil, levels)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	pricePlot := image.Rect(96, 42, 1184, 642-112)
	low, high, _ := candleRange(candles)
	padding := (high - low) * 0.06
	low -= padding
	high += padding
	includeLiquidationPrices(&low, &high, levels)
	for _, want := range []struct {
		price float64
		color color.NRGBA
	}{
		{100, coinGlassLongLineColor}, {101, coinGlassLongLineColor}, {102, coinGlassLongLineColor},
		{104.5, coinGlassShortLineColor}, {105.5, coinGlassShortLineColor}, {106.5, coinGlassShortLineColor},
	} {
		y := priceY(want.price, low, high, pricePlot)
		got := color.NRGBAModel.Convert(img.At(pricePlot.Min.X+20, y)).(color.NRGBA)
		if absInt(int(got.R)-int(want.color.R)) > 1 || absInt(int(got.G)-int(want.color.G)) > 1 || absInt(int(got.B)-int(want.color.B)) > 1 || got.A != want.color.A {
			t.Fatalf("CoinGlass line at %.2f y=%d pixel=%T %#v, want %#v", want.price, y, img.At(pricePlot.Min.X+20, y), got, want.color)
		}
	}
}

func TestCoinGlassPriceLabelsDrawAtBothLineEnds(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 40))
	background := color.RGBA{R: 248, G: 250, B: 252, A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
	plot := image.Rect(10, 10, 110, 30)
	drawCoinGlassPriceLabels(img, plot, 0, 100, 50, coinGlassLongLineColor)
	y := priceY(50, 0, 100, plot) - 4
	if got := color.RGBAModel.Convert(img.At(plot.Min.X+3, y)).(color.RGBA); got == background {
		t.Fatal("left CoinGlass price label was not drawn")
	}
	labelWidth := chartTextWidth(chartPriceLabel(50), 1)
	if got := color.RGBAModel.Convert(img.At(plot.Max.X-labelWidth-3, y)).(color.RGBA); got == background {
		t.Fatal("right CoinGlass price label was not drawn")
	}
}

func TestDrawWarningDiamondIsHollow(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	background := color.RGBA{R: 248, G: 250, B: 252, A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
	warning := color.RGBA{R: 225, G: 151, B: 35, A: 255}
	drawWarningDiamond(img, 10, 10, 4, warning)
	if got := color.RGBAModel.Convert(img.At(10, 10)).(color.RGBA); got != background {
		t.Fatalf("warning marker center = %#v, want background %#v", got, background)
	}
	if got := color.RGBAModel.Convert(img.At(10, 6)).(color.RGBA); got != warning {
		t.Fatalf("warning marker vertex = %#v, want %#v", got, warning)
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
	for _, char := range "预警激进稳健多单空" {
		if _, ok := chartGlyphs[char]; !ok {
			t.Fatalf("chart glyphs must include %q for the Chinese signal legend", char)
		}
	}
	if _, ok := chartGlyphs['+']; !ok {
		t.Fatal("chart glyphs must include + for the UTC+8 title")
	}
}

func TestCurrentAlertChartTaskUsesLatestKlines(t *testing.T) {
	originalFetch := fetchAlertKlines
	t.Cleanup(func() { fetchAlertKlines = originalFetch })
	var gotLimit int
	var gotUntil time.Time
	fetchAlertKlines = func(_ context.Context, _ string, _ string, limit int, until time.Time) ([]domain.MarketCandle, bool, error) {
		gotLimit = limit
		gotUntil = until
		base := time.Now().UTC().Truncate(time.Minute)
		return []domain.MarketCandle{{Time: base, Open: 100, High: 101, Low: 99, Close: 100.5, VolumeUSD: 10}}, false, nil
	}
	rtm := &alertRuntime{service: &Service{klineCache: make(map[string]klineCacheEntry), log: slog.Default()}}
	markerAt := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	task := rtm.currentAlertChartTask("BTCUSDT", "long", markerAt, 100)
	if !strings.Contains(task.PhotoCaption, "触发报价时间") || !strings.Contains(task.PhotoCaption, "原信号时间 2026-10-16 13:00:00 UTC+8") {
		t.Fatalf("manual chart caption = %q", task.PhotoCaption)
	}
	if _, err := task.Photo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gotUntil.IsZero() {
		t.Fatalf("manual chart must request the newest window, until=%v", gotUntil)
	}
	if alertChartCandleLimit != 720 || gotLimit != alertChartCandleLimit {
		t.Fatalf("manual chart candle limit = %d, want %d", gotLimit, alertChartCandleLimit)
	}
}

func TestAlertChartHistoryKeepsAllRowsInWindow(t *testing.T) {
	svc, _, st := alertTestService(t, "chart-history.db")
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Minute)
	for i := 0; i < 205; i++ {
		at := base.Add(-time.Duration(i) * time.Minute)
		if _, err := st.SaveLiquidationAlert(ctx, domain.LiquidationAlert{
			ID: fmt.Sprintf("chart-alert-%03d", i), Symbol: "BTCUSDT", Side: "long", TradeSide: "long",
			TriggeredAt: at, ReferencePrice: 100, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SaveTradeSignal(ctx, domain.TradeSignal{
			ID: fmt.Sprintf("chart-signal-%03d", i), AlertID: fmt.Sprintf("chart-alert-%03d", i),
			Symbol: "BTCUSDT", Kind: domain.SignalAggressive, Side: "long", CreatedAt: at.Add(30 * time.Second), ReferencePrice: 100,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := svc.alerts.alertChartHistory(ctx, "BTCUSDT", base.Add(-205*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 410 {
		t.Fatalf("chart history rows = %d, want all 410 alerts and signals", len(rows))
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
