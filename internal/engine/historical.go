package engine

import (
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/ml"
)

// HistoricalSamples is retained for API compatibility, but local ATR/wall
// replay is disabled. The service no longer trains or serves this model.
func HistoricalSamples(_ string, _ []domain.Candle, _ MapConfig) []ml.Sample {
	return nil
}

// HistoricalSamplesWithProfiles is retained for API compatibility, but local
// ATR/wall replay is disabled. CoinGlass is the sole liquidation-wall source.
func HistoricalSamplesWithProfiles(_ string, _ []domain.Candle, _ MapConfig, _ []domain.VolumeProfileSnapshot) []ml.Sample {
	return nil
}
