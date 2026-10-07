package engine

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestVolumeProfileFeatures(t *testing.T) {
	at := time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC)
	candles := []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: at.Add(-time.Minute), Open: 99, High: 101, Low: 98, Close: 100, OpenInterestUSD: 100}, {Exchange: "binance", Symbol: "BTCUSDT", Time: at, Open: 100, High: 103, Low: 99, Close: 102, OpenInterestUSD: 101}}
	result := MapResult{MarkPrice: 102, ATR: 2, Upper: &domain.Wall{DistanceATR: 1, IntensityUSD: 1000}, Lower: &domain.Wall{DistanceATR: 1, IntensityUSD: 900}}
	profile := &domain.VolumeProfileSnapshot{Symbol: "BTCUSDT", Time: at, VAL: 100, VAH: 104, Complete: true}
	features, err := BuildFeaturesWithProfile("BTCUSDT", candles, result, 0, 0, profile)
	if err != nil {
		t.Fatal(err)
	}
	if features.Values[27] != 1 || features.Values[28] != 1 || features.Values[29] != 2 || features.Values[30] != 1 || features.Values[31] != 0 {
		t.Fatalf("volume profile features=%v", features.Values[27:])
	}
	missing, err := BuildFeaturesWithProfile("BTCUSDT", candles, result, 0, 0, nil)
	if err != nil || missing.Values[31] != 1 || !missing.Missing["volume_profile"] {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}
