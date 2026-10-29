package engine

import (
	"testing"
)

func TestFeatureConstructionIsDisabled(t *testing.T) {
	if _, err := BuildFeaturesWithProfile("BTCUSDT", nil, MapResult{}, 0, 0, nil); err != ErrLocalPredictionDisabled {
		t.Fatalf("expected local prediction to be disabled, got %v", err)
	}
}
