package app

import (
	"context"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	alertGammaBTCAnchor = "BTCUSDT"
	alertGammaETHAnchor = "ETHUSDT"
	// Deribit Gamma is collected every five minutes. Use the same stale window
	// as the options page so the alert gate and the displayed value agree.
	alertGammaMaxAge = domain.OptionGammaStaleAfter
)

func alertGammaAnchorForSymbol(symbol string) string {
	if strings.EqualFold(strings.TrimSpace(symbol), alertGammaBTCAnchor) {
		return alertGammaBTCAnchor
	}
	return alertGammaETHAnchor
}

// alertGamma returns the ETH anchor used by the unfiltered overview. History
// pages call alertGammaForSymbol so BTCUSDT can use its own anchor.
func (s *Service) alertGamma(ctx context.Context) domain.AlertGamma {
	return s.alertGammaForAnchor(ctx, alertGammaETHAnchor)
}

// CurrentETHGamma exposes the shared, freshness-checked ETHUSDT snapshot to
// the embedded TVBot without coupling the trading package to the alert store.
func (s *Service) CurrentETHGamma(ctx context.Context) domain.AlertGamma {
	return s.alertGamma(ctx)
}

func (s *Service) alertGammaForSymbol(ctx context.Context, symbol string) domain.AlertGamma {
	return s.alertGammaForAnchor(ctx, alertGammaAnchorForSymbol(symbol))
}

// alertGammaForAnchor reads an anchor's in-memory Deribit snapshot first and
// falls back to the persisted latest value during startup or after a collector
// restart.
func (s *Service) alertGammaForAnchor(ctx context.Context, anchor string) domain.AlertGamma {
	out := domain.AlertGamma{AnchorSymbol: anchor, State: "unavailable"}
	if s == nil || s.dashboard == nil {
		return out
	}

	var snapshot domain.OptionGammaPoint
	var found bool
	s.dashboard.RLock()
	snapshot, found = s.dashboard.deribit[anchor]
	s.dashboard.RUnlock()
	if !found && s.store != nil {
		if stored, err := s.store.LatestOptionGamma(ctx, anchor); err == nil {
			snapshot = stored
			found = true
		}
	}
	if !found || snapshot.Time.IsZero() || snapshot.Gamma == nil {
		return out
	}

	state := strings.ToLower(strings.TrimSpace(snapshot.State))
	if state == "" {
		state = "ok"
	}
	out.State = state
	value := *snapshot.Gamma
	out.Gamma = &value
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

func (rtm *alertRuntime) gammaIgnored(symbol string) bool {
	if rtm == nil || rtm.service == nil {
		return false
	}
	return rtm.service.gammaPolicyForTenant(rtm.tenantID) == "ignore" && rtm.service.alertGamma(context.Background()).Ignored
}
