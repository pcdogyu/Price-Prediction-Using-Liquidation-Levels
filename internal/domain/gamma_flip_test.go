package domain

import (
	"testing"
	"time"
)

func TestEvaluateGammaFlipNearAndConfirmedRegime(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	flip := 105.0
	history := []GammaFlipSnapshot{
		{Symbol: "BTCUSDT", State: "ok", FlipState: "ok", Time: at.Add(-5 * time.Minute), SpotPrice: 100, GammaValue: 20, GammaFlip: &flip},
		{Symbol: "BTCUSDT", State: "ok", FlipState: "ok", Time: at, SpotPrice: 100, GammaValue: 10, GammaFlip: &flip},
	}
	decision := EvaluateGammaFlip(history, at, 1, 2)
	if !decision.Available || !decision.Confirmed || decision.Regime != "positive" || decision.Reason != "" {
		t.Fatalf("expected confirmed positive regime: %+v", decision)
	}

	nearFlip := 100.5
	decision = EvaluateGammaFlip([]GammaFlipSnapshot{{Symbol: "BTCUSDT", State: "ok", FlipState: "ok", Time: at, SpotPrice: 100, GammaValue: 10, GammaFlip: &nearFlip}}, at, 1, 1)
	if !decision.Available || !decision.NearFlip || decision.Confirmed || decision.Reason != "gamma_flip_near" {
		t.Fatalf("expected near-flip observation zone: %+v", decision)
	}
}

func TestEvaluateGammaFlipRejectsMixedStaleAndFutureSamples(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	flip := 105.0
	point := func(time time.Time, gamma float64) GammaFlipSnapshot {
		return GammaFlipSnapshot{Symbol: "ETHUSDT", State: "ok", FlipState: "ok", Time: time, SpotPrice: 100, GammaValue: gamma, GammaFlip: &flip}
	}

	mixed := EvaluateGammaFlip([]GammaFlipSnapshot{point(at.Add(-5*time.Minute), 1), point(at, -1)}, at, 1, 2)
	if !mixed.Available || mixed.Confirmed || mixed.Regime != "negative" || mixed.Reason != "gamma_flip_unconfirmed" {
		t.Fatalf("a sign change must reset the confirmation sequence: %+v", mixed)
	}

	stale := EvaluateGammaFlip([]GammaFlipSnapshot{point(at.Add(-16*time.Minute), 1)}, at, 1, 1)
	if stale.Available || stale.Snapshot != nil || stale.Reason != "gamma_flip_unavailable" {
		t.Fatalf("stale snapshot must be unavailable: %+v", stale)
	}

	future := EvaluateGammaFlip([]GammaFlipSnapshot{point(at.Add(time.Minute), 1)}, at, 1, 1)
	if future.Available {
		t.Fatalf("future Gamma data must not leak into the decision: %+v", future)
	}
}
