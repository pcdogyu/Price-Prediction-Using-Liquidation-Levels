package server

import (
	"context"
	"math"
	"strings"

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

func (s *Server) SetGammaProvider(provider GammaProvider) {
	if s == nil {
		return
	}
	s.GammaProvider = provider
}

func (s *Server) applyTVOrderGammaPolicy(ctx context.Context, signal *tvbottrading.Signal, cfg tvbotconfig.Config) bool {
	if signal == nil || signal.PositionEffect != tvbottrading.PositionEffectOpen || s == nil || s.GammaProvider == nil {
		return false
	}
	snapshot := s.GammaProvider(ctx)
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
