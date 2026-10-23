package server

import (
	"context"
	"testing"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVOrderGammaPolicyFailsOpenForPositiveUnavailableAndStaleGamma(t *testing.T) {
	negative := -0.25
	for _, tc := range []struct {
		name     string
		snapshot GammaSnapshot
	}{
		{name: "positive", snapshot: GammaSnapshot{State: "ok", Value: float64Pointer(0.25)}},
		{name: "unavailable", snapshot: GammaSnapshot{State: "unavailable", Value: &negative}},
		{name: "stale", snapshot: GammaSnapshot{State: "stale", Value: &negative}},
		{name: "missing", snapshot: GammaSnapshot{State: "ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tvbotconfig.Default()
			cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyIgnore
			signal := tvbottrading.Signal{Action: tvbottrading.ActionLong, PositionEffect: tvbottrading.PositionEffectOpen, PositionSide: tvbottrading.PositionSideLong}
			srv := &Server{GammaProvider: func(context.Context) GammaSnapshot { return tc.snapshot }}
			if srv.applyTVOrderGammaPolicy(context.Background(), &signal, cfg) {
				t.Fatalf("%s Gamma should pass through", tc.name)
			}
		})
	}
}

func TestTVOrderGammaPolicyFlipPreservesOriginalActionAndEntryMetadata(t *testing.T) {
	negative := -0.25
	cfg := tvbotconfig.Default()
	cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyFlip
	signal := tvbottrading.Signal{
		Action:         tvbottrading.ActionLong,
		SourceAction:   tvbottrading.ActionLong,
		PositionEffect: tvbottrading.PositionEffectOpen,
		PositionSide:   tvbottrading.PositionSideLong,
		OrderIntent:    "entry_long|script=4.1.1|adx=31",
		Intent:         "entry_long",
	}
	srv := &Server{GammaProvider: func(context.Context) GammaSnapshot { return GammaSnapshot{State: "ok", Value: &negative} }}
	if srv.applyTVOrderGammaPolicy(context.Background(), &signal, cfg) {
		t.Fatal("flip policy should not ignore the signal")
	}
	if signal.Action != tvbottrading.ActionShort || signal.PositionSide != tvbottrading.PositionSideShort || signal.SourceAction != tvbottrading.ActionLong {
		t.Fatalf("flip did not preserve original action while changing execution side: %#v", signal)
	}
	if signal.OrderIntent != "entry_short|script=4.1.1|adx=31" || signal.Intent != "entry_short" {
		t.Fatalf("flip did not update entry intent: %#v", signal)
	}
}

func TestTVOrderGammaPolicyNeverChangesCloseSignals(t *testing.T) {
	negative := -0.25
	cfg := tvbotconfig.Default()
	cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyFlip
	signal := tvbottrading.Signal{
		Action:         tvbottrading.ActionShort,
		SourceAction:   tvbottrading.ActionShort,
		PositionEffect: tvbottrading.PositionEffectClose,
		PositionSide:   tvbottrading.PositionSideLong,
		OrderIntent:    "close_long",
	}
	srv := &Server{GammaProvider: func(context.Context) GammaSnapshot { return GammaSnapshot{State: "ok", Value: &negative} }}
	if srv.applyTVOrderGammaPolicy(context.Background(), &signal, cfg) {
		t.Fatal("close signal should never be ignored")
	}
	if signal.Action != tvbottrading.ActionShort || signal.PositionSide != tvbottrading.PositionSideLong || signal.OrderIntent != "close_long" {
		t.Fatalf("close signal changed under negative Gamma: %#v", signal)
	}
}
