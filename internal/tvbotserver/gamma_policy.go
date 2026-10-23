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
	case tvbotconfig.GammaNegativePolicyFlip:
		signal.Action = oppositeTradingSide(signal.Action)
		signal.PositionSide = oppositePositionSide(signal.PositionSide)
		signal.OrderIntent = flipTVOrderEntryIntent(signal.OrderIntent, signal.Action)
		if strings.TrimSpace(signal.Intent) != "" {
			signal.Intent = flipTVOrderEntryIntent(signal.Intent, signal.Action)
		}
	}
	return false
}

func flipTVOrderEntryIntent(raw string, action tvbottrading.Side) string {
	parts := strings.Split(strings.TrimSpace(raw), "|")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return tvOrderEntryIntent(action)
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "entry_long":
		parts[0] = "entry_short"
	case "entry_short":
		parts[0] = "entry_long"
	default:
		parts[0] = tvOrderEntryIntent(action)
	}
	return strings.Join(parts, "|")
}

func tvOrderEntryIntent(action tvbottrading.Side) string {
	if action == tvbottrading.ActionShort {
		return "entry_short"
	}
	return "entry_long"
}
