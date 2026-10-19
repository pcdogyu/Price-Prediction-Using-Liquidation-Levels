package app

import (
	"context"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	alertGammaAnchor = "ETHUSDT"
	// Gamma is collected every five minutes. Allow a few missed cycles for a
	// usable snapshot, but never turn an old value into a fresh zero.
	alertGammaMaxAge = 15 * time.Minute
)

// alertGamma returns the ETH options GEX regime used as the common gate for
// every liquidation pair. It reads the in-memory snapshot first and falls back
// to the persisted latest value during startup or after a collector restart.
func (s *Service) alertGamma(ctx context.Context) domain.AlertGamma {
	out := domain.AlertGamma{AnchorSymbol: alertGammaAnchor, State: "unavailable"}
	if s == nil || s.dashboard == nil {
		return out
	}

	var snapshot domain.GammaView
	var found bool
	s.dashboard.RLock()
	snapshot, found = s.dashboard.gamma[alertGammaAnchor]
	s.dashboard.RUnlock()
	if !found && s.store != nil {
		if stored, err := s.store.LatestGamma(ctx, alertGammaAnchor); err == nil {
			snapshot = stored
			found = true
		}
	}
	if !found || snapshot.Time.IsZero() {
		return out
	}

	state := strings.ToLower(strings.TrimSpace(snapshot.State))
	if state == "" {
		state = "ok"
	}
	out.State = state
	value := snapshot.NetGEXUSD
	out.NetGEXUSD = &value
	updated := snapshot.Time.UTC()
	out.UpdatedAt = &updated

	now := time.Now().UTC()
	if updated.After(now.Add(2*time.Minute)) || now.Sub(updated) > alertGammaMaxAge {
		out.State = "stale"
		return out
	}
	if state == "ok" || state == "partial" {
		out.Ignored = value < 0
	}
	return out
}

func (rtm *alertRuntime) ethGammaIgnored() bool {
	if rtm == nil || rtm.service == nil {
		return false
	}
	return rtm.service.alertGamma(context.Background()).Ignored
}
