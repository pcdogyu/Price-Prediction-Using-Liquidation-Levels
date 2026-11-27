package server

import (
	"context"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVOrderGammaFlipGuardDoesNotBlockNearZoneOrUnconfirmedSignals(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cfg := tvbotconfig.Default()
	makeSnapshot := func(at time.Time, spot, gamma, flip float64) domain.GammaFlipSnapshot {
		return domain.GammaFlipSnapshot{Symbol: "BTCUSDT", State: "ok", FlipState: "ok", Time: at, SpotPrice: spot, GammaValue: gamma, GammaFlip: &flip}
	}
	openSignal := func() tvbottrading.Signal {
		return tvbottrading.Signal{Action: tvbottrading.ActionLong, PositionEffect: tvbottrading.PositionEffectOpen, PositionSide: tvbottrading.PositionSideLong, Ticker: "BTCUSDT"}
	}

	t.Run("near flip passes through", func(t *testing.T) {
		current := makeSnapshot(now, 100, 10, 100.5)
		srv := &Server{
			GammaFlipProvider: func(context.Context, string) domain.GammaFlipSnapshot { return current },
			GammaFlipHistoryProvider: func(context.Context, string, time.Time, time.Time) ([]domain.GammaFlipSnapshot, error) {
				return []domain.GammaFlipSnapshot{current}, nil
			},
		}
		signal := openSignal()
		if got := srv.applyTVOrderGammaFlipGuard(context.Background(), &signal, cfg, now); got != "" {
			t.Fatalf("near-flip state must not block the order, reason=%q", got)
		}
	})

	t.Run("single sample unconfirmed passes through", func(t *testing.T) {
		current := makeSnapshot(now, 100, 10, 105)
		srv := &Server{
			GammaFlipProvider: func(context.Context, string) domain.GammaFlipSnapshot { return current },
			GammaFlipHistoryProvider: func(context.Context, string, time.Time, time.Time) ([]domain.GammaFlipSnapshot, error) {
				return []domain.GammaFlipSnapshot{current}, nil
			},
		}
		signal := openSignal()
		if got := srv.applyTVOrderGammaFlipGuard(context.Background(), &signal, cfg, now); got != "" {
			t.Fatalf("unconfirmed state must not block the order, reason=%q", got)
		}
	})

	t.Run("two samples confirm", func(t *testing.T) {
		current := makeSnapshot(now, 100, 10, 105)
		prior := makeSnapshot(now.Add(-5*time.Minute), 100, 20, 105)
		srv := &Server{
			GammaFlipProvider: func(context.Context, string) domain.GammaFlipSnapshot { return current },
			GammaFlipHistoryProvider: func(context.Context, string, time.Time, time.Time) ([]domain.GammaFlipSnapshot, error) {
				return []domain.GammaFlipSnapshot{prior, current}, nil
			},
		}
		signal := openSignal()
		if got := srv.applyTVOrderGammaFlipGuard(context.Background(), &signal, cfg, now); got != "" {
			t.Fatalf("confirmed regime should pass, reason=%q", got)
		}
	})

	t.Run("unavailable data fails open", func(t *testing.T) {
		srv := &Server{GammaFlipProvider: func(context.Context, string) domain.GammaFlipSnapshot {
			return domain.GammaFlipSnapshot{Symbol: "BTCUSDT", State: "unavailable", FlipState: "unavailable"}
		}}
		signal := openSignal()
		if got := srv.applyTVOrderGammaFlipGuard(context.Background(), &signal, cfg, now); got != "" {
			t.Fatalf("unavailable Gamma should pass through, reason=%q", got)
		}
	})
}

func TestTVOrderGammaFlipStatesKeepNegativeGammaPolicyIndependent(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	negative := -0.25
	for _, tc := range []struct {
		name string
		spot float64
		flip float64
	}{
		{name: "near flip", spot: 100, flip: 100.5},
		{name: "unconfirmed", spot: 100, flip: 105},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := domain.GammaFlipSnapshot{
				Symbol: "BTCUSDT", State: "ok", FlipState: "ok", Time: now,
				SpotPrice: tc.spot, GammaValue: negative, GammaFlip: &tc.flip,
			}
			srv := &Server{
				GammaFlipProvider: func(context.Context, string) domain.GammaFlipSnapshot { return current },
				GammaFlipHistoryProvider: func(context.Context, string, time.Time, time.Time) ([]domain.GammaFlipSnapshot, error) {
					return []domain.GammaFlipSnapshot{current}, nil
				},
			}
			signal := tvbottrading.Signal{
				Action: tvbottrading.ActionLong, PositionEffect: tvbottrading.PositionEffectOpen,
				PositionSide: tvbottrading.PositionSideLong, Ticker: "BTCUSDT",
				GammaValue: &negative, GammaState: "ok",
			}
			cfg := tvbotconfig.Default()
			cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyPassthrough
			if got := srv.applyTVOrderGammaFlipGuard(context.Background(), &signal, cfg, now); got != "" {
				t.Fatalf("Gamma Flip state must not block the order, reason=%q", got)
			}
			if srv.applyTVOrderGammaPolicy(context.Background(), &signal, cfg) {
				t.Fatal("the existing passthrough negative-Gamma policy should continue to allow the signal")
			}

			cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyIgnore
			if !srv.applyTVOrderGammaPolicy(context.Background(), &signal, cfg) {
				t.Fatal("the existing ignore negative-Gamma policy should continue to block the signal")
			}
		})
	}
}

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

func TestTVOrderGammaPolicyAdaptivePassesThroughOrdinaryNegativeGamma(t *testing.T) {
	negative := -0.25
	cfg := tvbotconfig.Default()
	cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyAdaptive
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
		t.Fatal("adaptive policy should not ignore the signal")
	}
	if signal.Action != tvbottrading.ActionLong || signal.PositionSide != tvbottrading.PositionSideLong || signal.SourceAction != tvbottrading.ActionLong {
		t.Fatalf("adaptive changed the ordinary signal direction: %#v", signal)
	}
	if signal.OrderIntent != "entry_long|script=4.1.1|adx=31" || signal.Intent != "entry_long" {
		t.Fatalf("adaptive changed the ordinary entry intent: %#v", signal)
	}
}

func TestTVOrderGammaPolicyUsesSignalTimeGammaBeforeLiveSnapshot(t *testing.T) {
	negative := -0.25
	positive := 0.25
	cfg := tvbotconfig.Default()
	cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyIgnore
	signal := tvbottrading.Signal{
		Action:         tvbottrading.ActionLong,
		PositionEffect: tvbottrading.PositionEffectOpen,
		PositionSide:   tvbottrading.PositionSideLong,
		GammaValue:     &negative,
		GammaState:     "ok",
	}
	srv := &Server{GammaProvider: func(context.Context) GammaSnapshot {
		return GammaSnapshot{State: "ok", Value: &positive}
	}}
	if !srv.applyTVOrderGammaPolicy(context.Background(), &signal, cfg) {
		t.Fatal("negative Gamma at signal time must still block an opening after live Gamma recovers")
	}
}

func TestNegativeGammaCloseActiveRequiresHealthyNegativeGammaAndIgnorePolicy(t *testing.T) {
	negative := -0.25
	positive := 0.25
	for _, tc := range []struct {
		name     string
		policy   string
		snapshot GammaSnapshot
		want     bool
	}{
		{name: "negative ignore", policy: tvbotconfig.GammaNegativePolicyIgnore, snapshot: GammaSnapshot{State: "ok", Value: &negative}, want: true},
		{name: "positive ignore", policy: tvbotconfig.GammaNegativePolicyIgnore, snapshot: GammaSnapshot{State: "ok", Value: &positive}},
		{name: "negative passthrough", policy: tvbotconfig.GammaNegativePolicyPassthrough, snapshot: GammaSnapshot{State: "ok", Value: &negative}},
		{name: "negative stale", policy: tvbotconfig.GammaNegativePolicyIgnore, snapshot: GammaSnapshot{State: "stale", Value: &negative}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tvbotconfig.Default()
			cfg.Trading.GammaNegativePolicy = tc.policy
			srv := &Server{GammaProvider: func(context.Context) GammaSnapshot { return tc.snapshot }}
			if got := srv.negativeGammaCloseActive(context.Background(), cfg); got != tc.want {
				t.Fatalf("negative Gamma close active=%v want %v", got, tc.want)
			}
		})
	}
}

func TestTVOrderGammaPolicyNeverChangesCloseSignals(t *testing.T) {
	negative := -0.25
	cfg := tvbotconfig.Default()
	cfg.Trading.GammaNegativePolicy = tvbotconfig.GammaNegativePolicyAdaptive
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

func TestManualAlertOrderBypassesNegativeGammaPolicy(t *testing.T) {
	for _, condition := range []string{"manual_liquidation_trigger", "value_area_manual"} {
		t.Run(condition, func(t *testing.T) {
			signal := tvbottrading.Signal{
				Action:         tvbottrading.ActionLong,
				PositionEffect: tvbottrading.PositionEffectOpen,
				PositionSide:   tvbottrading.PositionSideLong,
				Condition:      condition,
			}
			if !manualAlertOrderBypassesGamma(signal) {
				t.Fatalf("manual alert condition %q should bypass negative Gamma", condition)
			}
		})
	}
	for _, condition := range []string{"steady_liquidation_confirmation", "manual_liquidation_trigger"} {
		t.Run(condition+"_close", func(t *testing.T) {
			signal := tvbottrading.Signal{
				Action:         tvbottrading.ActionLong,
				PositionEffect: tvbottrading.PositionEffectClose,
				PositionSide:   tvbottrading.PositionSideShort,
				Condition:      condition,
			}
			if manualAlertOrderBypassesGamma(signal) {
				t.Fatalf("close condition %q should not use the manual opening bypass", condition)
			}
		})
	}
}
