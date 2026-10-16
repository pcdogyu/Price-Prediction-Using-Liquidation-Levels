package app

import (
	"bytes"
	"image/png"
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
	}, base.Add(90*time.Second), 103, "long")
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
