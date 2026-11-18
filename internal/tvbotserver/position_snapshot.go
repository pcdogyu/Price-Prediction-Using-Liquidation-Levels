package server

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type analysisPositionPnL struct {
	Symbol           string `json:"symbol"`
	Side             string `json:"side"`
	Size             string `json:"size"`
	EntryPrice       string `json:"entry_price,omitempty"`
	MarkPrice        string `json:"mark_price,omitempty"`
	UnrealizedPnL    string `json:"unrealized_pnl,omitempty"`
	PnLCurrency      string `json:"pnl_currency,omitempty"`
	PnLRatio         string `json:"pnl_ratio,omitempty"`
	Leverage         string `json:"leverage,omitempty"`
	LiquidationPrice string `json:"liquidation_price,omitempty"`
}

type analysisPnLTotal struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type analysisPositionSnapshotPayload struct {
	PnLTotals []analysisPnLTotal    `json:"pnl_totals"`
	Positions []analysisPositionPnL `json:"positions"`
}

type analysisEquityPeak struct {
	Time           time.Time             `json:"time"`
	Equity         string                `json:"equity"`
	Currency       string                `json:"currency"`
	PositionStatus string                `json:"position_status"`
	PnLTotals      []analysisPnLTotal    `json:"pnl_totals"`
	Positions      []analysisPositionPnL `json:"positions"`
}

func positionPnLFromOKX(position tvbotokx.Position) analysisPositionPnL {
	side := strings.ToLower(strings.TrimSpace(position.PosSide))
	if side == "" || side == "net" {
		if size, err := strconv.ParseFloat(strings.TrimSpace(position.Pos), 64); err == nil {
			if size > 0 {
				side = "long"
			} else if size < 0 {
				side = "short"
			}
		}
	}
	currency := strings.ToUpper(strings.TrimSpace(position.Ccy))
	if currency == "" {
		parts := strings.Split(strings.ToUpper(strings.TrimSpace(position.InstID)), "-")
		if len(parts) >= 2 {
			currency = parts[len(parts)-2]
		}
	}
	return analysisPositionPnL{
		Symbol:           strings.ToUpper(strings.TrimSpace(position.InstID)),
		Side:             side,
		Size:             strings.TrimSpace(position.Pos),
		EntryPrice:       strings.TrimSpace(position.AvgPx),
		MarkPrice:        strings.TrimSpace(position.MarkPx),
		UnrealizedPnL:    strings.TrimSpace(position.Upl),
		PnLCurrency:      currency,
		PnLRatio:         strings.TrimSpace(position.UplRatio),
		Leverage:         strings.TrimSpace(position.Lever),
		LiquidationPrice: strings.TrimSpace(position.LiqPx),
	}
}

func positionSnapshotPayload(positions []analysisPositionPnL) analysisPositionSnapshotPayload {
	if positions == nil {
		positions = []analysisPositionPnL{}
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Symbol == positions[j].Symbol {
			return positions[i].Side < positions[j].Side
		}
		return positions[i].Symbol < positions[j].Symbol
	})
	totals := map[string]*big.Rat{}
	decimals := map[string]int{}
	for _, position := range positions {
		currency := strings.ToUpper(strings.TrimSpace(position.PnLCurrency))
		value, ok := new(big.Rat).SetString(strings.TrimSpace(position.UnrealizedPnL))
		if !ok || currency == "" {
			continue
		}
		if totals[currency] == nil {
			totals[currency] = new(big.Rat)
		}
		totals[currency].Add(totals[currency], value)
		if count := decimalsFromDecimalString(position.UnrealizedPnL); count > decimals[currency] {
			decimals[currency] = count
		}
	}
	currencies := make([]string, 0, len(totals))
	for currency := range totals {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	pnlTotals := make([]analysisPnLTotal, 0, len(currencies))
	for _, currency := range currencies {
		pnlTotals = append(pnlTotals, analysisPnLTotal{
			Currency: currency,
			Value:    trimDecimalZeros(totals[currency].FloatString(decimals[currency])),
		})
	}
	return analysisPositionSnapshotPayload{PnLTotals: pnlTotals, Positions: positions}
}

func (s *Server) capturePositionSnapshot(ctx context.Context, cfg tvbotconfig.Config, exchange, requestedAPIID, envName string, observedAt time.Time) error {
	apiID := strings.TrimSpace(requestedAPIID)
	positions := make([]analysisPositionPnL, 0)
	var err error
	switch tvbottrading.NormalizeExchange(exchange) {
	case tvbottrading.ExchangeOKX:
		store := s.okxCredentialsForSampling(envName)
		if store == nil {
			err = errors.New("OKX credential store is not configured")
		} else {
			var credentials tvbotokx.Credentials
			credentials, apiID, err = store.OKXCredentials(requestedAPIID)
			client := s.analysisOKXClient(cfg, credentials)
			var raw []tvbotokx.Position
			if err == nil {
				raw, _, err = client.Positions(ctx, "SWAP")
			}
			if err == nil {
				for _, position := range openPositions(raw) {
					positions = append(positions, positionPnLFromOKX(position))
				}
			}
		}
	case tvbottrading.ExchangeBinance:
		store := s.binanceCredentialsForSampling(envName)
		if store == nil {
			err = errors.New("Binance credential store is not configured")
		} else {
			var credentials tvbotbinance.Credentials
			credentials, apiID, err = store.BinanceCredentials(requestedAPIID)
			client := s.analysisBinanceClient(cfg, credentials)
			var raw []tvbotbinance.Position
			if err == nil {
				raw, err = client.Positions(ctx, "")
			}
			if err == nil {
				for _, position := range raw {
					converted := binancePositionToOKX(position)
					if isOpenPosition(converted.Pos) {
						positions = append(positions, positionPnLFromOKX(converted))
					}
				}
			}
		}
	case tvbottrading.ExchangeGate:
		store := s.gateCredentialsForSampling(envName)
		if store == nil {
			err = errors.New("Gate credential store is not configured")
		} else {
			var credentials tvbotgate.Credentials
			credentials, apiID, err = store.GateCredentials(requestedAPIID)
			client := tvbotgate.Client{BaseURL: cfg.GateBaseURL(), Credentials: credentials, HTTPClient: s.GateHTTPClient}
			var raw []tvbotgate.Position
			if err == nil {
				raw, err = client.Positions(ctx, "")
			}
			if err == nil {
				for _, position := range raw {
					converted := gatePositionToOKX(position)
					if isOpenPosition(converted.Pos) {
						positions = append(positions, positionPnLFromOKX(converted))
					}
				}
			}
		}
	default:
		err = errors.New("unsupported exchange")
	}
	if apiID == "" {
		apiID = strings.TrimSpace(requestedAPIID)
	}
	status := "ok"
	if err != nil {
		status = "unavailable"
		positions = []analysisPositionPnL{}
	}
	payload, marshalErr := json.Marshal(positionSnapshotPayload(positions))
	if marshalErr != nil {
		return marshalErr
	}
	if s.Orders == nil {
		if err != nil {
			return err
		}
		return nil
	}
	storeErr := s.Orders.UpsertUSDTPositionSnapshot(tvbotstorage.USDTPositionSnapshot{
		Exchange:    tvbottrading.NormalizeExchange(exchange),
		APIID:       apiID,
		Env:         envName,
		ObservedAt:  observedAt.UTC(),
		Status:      status,
		PayloadJSON: string(payload),
	})
	if storeErr != nil {
		return storeErr
	}
	return err
}

func (s *Server) attachBalancePeaks(out *exchangeBalanceOverview, minutes int, now time.Time) {
	if s.Orders == nil || out == nil || !out.Configured || strings.TrimSpace(out.APIID) == "" {
		return
	}
	periodSince := now.Add(-time.Duration(minutes) * time.Minute)
	if minutes <= 0 {
		periodSince = now.Add(-5 * time.Minute)
	}
	out.PeriodPeak = s.balancePeak(out.Exchange, out.APIID, out.Env, periodSince)
	out.AllTimePeak = s.balancePeak(out.Exchange, out.APIID, out.Env, time.Time{})
}

func (s *Server) balancePeak(exchange, apiID, env string, since time.Time) *analysisEquityPeak {
	snapshot, err := s.Orders.FindExchangeUSDTBalancePeak(exchange, apiID, env, since)
	if err != nil {
		return nil
	}
	peak := &analysisEquityPeak{
		Time:           time.UnixMilli(snapshot.BucketTS).UTC(),
		Equity:         strings.TrimSpace(snapshot.Eq),
		Currency:       strings.ToUpper(firstNonEmpty(snapshot.Currency, "USDT")),
		PositionStatus: "missing",
		PnLTotals:      []analysisPnLTotal{},
		Positions:      []analysisPositionPnL{},
	}
	positionSnapshot, err := s.Orders.GetUSDTPositionSnapshot(exchange, apiID, env, snapshot.BucketTS)
	if err != nil {
		if !tvbotstorage.IsMissingUSDTPositionSnapshot(err) && s.Logger != nil {
			s.Logger.Warn("failed to load equity peak position snapshot", "exchange", exchange, "api_id", apiID, "env", env, "error", err)
		}
		return peak
	}
	peak.PositionStatus = positionSnapshot.Status
	if positionSnapshot.Status != "ok" {
		return peak
	}
	var payload analysisPositionSnapshotPayload
	if err := json.Unmarshal([]byte(positionSnapshot.PayloadJSON), &payload); err != nil {
		peak.PositionStatus = "unavailable"
		if s.Logger != nil {
			s.Logger.Warn("failed to decode equity peak position snapshot", "exchange", exchange, "api_id", apiID, "env", env, "error", err)
		}
		return peak
	}
	if payload.PnLTotals == nil {
		payload.PnLTotals = []analysisPnLTotal{}
	}
	if payload.Positions == nil {
		payload.Positions = []analysisPositionPnL{}
	}
	peak.PnLTotals = payload.PnLTotals
	peak.Positions = payload.Positions
	return peak
}
