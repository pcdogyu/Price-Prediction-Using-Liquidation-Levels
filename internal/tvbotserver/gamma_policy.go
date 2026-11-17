package server

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

// GammaSnapshot is the shared ETHUSDT Deribit Gamma value used by every
// /tvorder entry point. A nil value or a non-healthy state is fail-open.
type GammaSnapshot struct {
	State string
	Value *float64
}

type GammaProvider func(context.Context) GammaSnapshot

type GammaFlipSnapshotProvider func(context.Context, string) domain.GammaFlipSnapshot

type GammaFlipHistoryProvider func(context.Context, string, time.Time, time.Time) ([]domain.GammaFlipSnapshot, error)

func (s *Server) applyTVOrderGammaFlipGuard(ctx context.Context, signal *tvbottrading.Signal, cfg tvbotconfig.Config, at time.Time) string {
	if signal == nil || signal.PositionEffect != tvbottrading.PositionEffectOpen ||
		!cfg.Trading.GammaFlipGuardEnabled || s == nil || (s.GammaFlipProvider == nil && signal.GammaFlipSnapshot == nil) ||
		manualAlertOrderBypassesGamma(*signal) {
		return ""
	}
	if signal.GammaFlipSnapshot == nil && s.GammaFlipProvider != nil {
		snapshot := s.GammaFlipProvider(ctx, firstNonEmptyString(signal.Ticker, signal.Coinpair))
		signal.GammaFlipSnapshot = &snapshot
	}
	from := at.Add(-2 * time.Hour)
	history := []domain.GammaFlipSnapshot{}
	if s.GammaFlipHistoryProvider != nil {
		if rows, err := s.GammaFlipHistoryProvider(ctx, firstNonEmptyString(signal.Ticker, signal.Coinpair), from, at.Add(time.Millisecond)); err == nil {
			history = rows
		}
	}
	if signal.GammaFlipSnapshot != nil && !signal.GammaFlipSnapshot.Time.IsZero() {
		found := false
		for i := range history {
			if history[i].Time.Equal(signal.GammaFlipSnapshot.Time) {
				history[i] = *signal.GammaFlipSnapshot
				found = true
				break
			}
		}
		if !found {
			history = append(history, *signal.GammaFlipSnapshot)
		}
	}
	decision := domain.EvaluateGammaFlip(history, at, cfg.Trading.GammaFlipNearPct, cfg.Trading.GammaFlipConfirmSamples)
	if !decision.Available {
		return ""
	}
	return decision.Reason
}

func (s *Server) SetGammaProvider(provider GammaProvider) {
	if s == nil {
		return
	}
	s.GammaProvider = provider
}

func (s *Server) SetGammaFlipProviders(snapshot GammaFlipSnapshotProvider, history GammaFlipHistoryProvider) {
	if s == nil {
		return
	}
	s.GammaFlipProvider = snapshot
	s.GammaFlipHistoryProvider = history
}

func (s *Server) applyTVOrderGammaPolicy(ctx context.Context, signal *tvbottrading.Signal, cfg tvbotconfig.Config) bool {
	if signal == nil || signal.PositionEffect != tvbottrading.PositionEffectOpen || s == nil || (signal.GammaValue == nil && s.GammaProvider == nil) {
		return false
	}
	snapshot := GammaSnapshot{}
	// Prefer the immutable Gamma decision carried by the alert. A webhook can
	// be queued for several seconds; consulting only the latest snapshot here
	// could turn a negative-Gamma opening into an allowed opening after Gamma
	// briefly recovers. Older/manual payloads without these fields retain the
	// live-provider fallback below.
	if signal.GammaValue != nil {
		snapshot.State = signal.GammaState
		snapshot.Value = signal.GammaValue
	} else {
		snapshot = s.GammaProvider(ctx)
	}
	state := strings.ToLower(strings.TrimSpace(snapshot.State))
	if state != "ok" && state != "partial" || snapshot.Value == nil || math.IsNaN(*snapshot.Value) || math.IsInf(*snapshot.Value, 0) || *snapshot.Value >= 0 {
		return false
	}

	switch cfg.Trading.GammaNegativePolicy {
	case tvbotconfig.GammaNegativePolicyIgnore:
		return true
	}
	return false
}

// negativeGammaCloseActive makes the configured "ignore" policy a hard-flat
// mode for existing positions as well as a gate for new openings. It is
// deliberately fail-closed for the trigger: an unavailable or stale Gamma
// snapshot must never create an automatic close storm.
func (s *Server) negativeGammaCloseActive(ctx context.Context, cfg tvbotconfig.Config) bool {
	if s == nil || s.GammaProvider == nil || cfg.Trading.GammaNegativePolicy != tvbotconfig.GammaNegativePolicyIgnore {
		return false
	}
	snapshot := s.GammaProvider(ctx)
	state := strings.ToLower(strings.TrimSpace(snapshot.State))
	return (state == "ok" || state == "partial") && snapshot.Value != nil && !math.IsNaN(*snapshot.Value) && !math.IsInf(*snapshot.Value, 0) && *snapshot.Value < 0
}

// manualAlertOrderBypassesGamma identifies order requests created by the
// authenticated alerts-page "触发下单" action. These signals still pass
// through the rest of the /tvorder validation and risk pipeline, but the
// operator explicitly requested the persisted alert direction, so the
// automatic negative-Gamma opening gate must not ignore or flip it.
func manualAlertOrderBypassesGamma(signal tvbottrading.Signal) bool {
	if signal.PositionEffect != tvbottrading.PositionEffectOpen {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(signal.Condition)) {
	case "manual_liquidation_trigger", "value_area_manual":
		return true
	default:
		return false
	}
}

func tvOrderEntryIntent(action tvbottrading.Side) string {
	if action == tvbottrading.ActionShort {
		return "entry_short"
	}
	return "entry_long"
}
