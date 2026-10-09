package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/exchange"
)

type optionGammaAttempt struct {
	at    time.Time
	error string
}

func (s *Service) recordOptionGamma(ctx context.Context, symbol string, point domain.OptionGammaPoint, err error) {
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		err = s.store.SaveOptionGamma(ctx, point)
	}
	attempt := optionGammaAttempt{at: time.Now().UTC()}
	if err != nil {
		attempt.error = err.Error()
		s.health.Fail("deribit_options_"+symbol, err)
		s.log.Warn("Deribit Gamma collection failed", "symbol", symbol, "error", err)
	} else if point.State == "partial" {
		s.health.Touch("deribit_options_"+symbol, float64(point.Contracts)/float64(point.SelectedContracts))
		s.health.Fail("deribit_options_"+symbol, errors.New(point.Warning))
	} else {
		s.health.Touch("deribit_options_"+symbol, float64(point.Contracts)/float64(point.SelectedContracts))
	}
	s.dashboard.Lock()
	s.dashboard.deribitAttempts[symbol] = attempt
	if err == nil {
		s.dashboard.deribit[symbol] = point
	}
	s.dashboard.Unlock()
}

func (s *Service) deribitGammaLoop(ctx context.Context) {
	client := exchange.NewDeribitClient()
	ticker := time.NewTicker(time.Duration(domain.OptionGammaRefreshSeconds) * time.Second)
	defer ticker.Stop()
	for {
		if err := s.store.PruneOptionGamma(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.log.Warn("Deribit Gamma retention failed", "error", err)
		}
		for _, currency := range []string{"BTC", "ETH"} {
			cc, cancel := context.WithTimeout(ctx, 45*time.Second)
			point, err := client.Gamma(cc, currency, time.Now().UTC())
			cancel()
			s.recordOptionGamma(ctx, currency+"USDT", point, err)
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) Options(ctx context.Context, hours int) (domain.OptionsView, error) {
	if hours < 1 || hours > domain.OptionGammaMaxHours {
		return domain.OptionsView{}, fmt.Errorf("hours must be between 1 and %d", domain.OptionGammaMaxHours)
	}
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(domain.OptionGammaMaxHours) * time.Hour)
	view := domain.OptionsView{Source: "deribit", Method: domain.DeribitGammaMethod, From: now.Add(-time.Duration(hours) * time.Hour), To: now, Hours: hours, RefreshSeconds: domain.OptionGammaRefreshSeconds, RetentionDays: domain.OptionGammaRetentionDays, Series: []domain.OptionGammaSeries{}}
	var err error
	view.AvailableFrom, err = s.store.OptionGammaAvailableFrom(ctx, cutoff)
	if err != nil {
		return view, err
	}
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		item := domain.OptionGammaSeries{Symbol: symbol, Underlying: strings.TrimSuffix(symbol, "USDT"), State: "unavailable"}
		item.Points, err = s.store.OptionGammaHistory(ctx, symbol, view.From, view.To)
		if err != nil {
			return view, err
		}
		s.dashboard.RLock()
		point, hasPoint := s.dashboard.deribit[symbol]
		attempt, hasAttempt := s.dashboard.deribitAttempts[symbol]
		s.dashboard.RUnlock()
		if !hasPoint {
			point, err = s.store.LatestOptionGamma(ctx, symbol)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return view, err
			}
			hasPoint = err == nil
		}
		if hasAttempt {
			item.LastAttempt = &attempt.at
			item.LastError = attempt.error
		}
		if hasPoint && !point.Time.Before(cutoff) {
			item.Latest = &point
			item.State = point.State
			if now.Sub(point.Time) > 3*time.Minute {
				item.State = "stale"
			} else if item.LastError != "" {
				item.State = "partial"
			}
		}
		view.Series = append(view.Series, item)
	}
	return view, nil
}
