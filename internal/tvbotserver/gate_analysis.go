package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	gateAnalysisPageLimit = 1000
	gateAnalysisMaxPages  = 20
)

func (s *Server) analysisGateCredentials(cfg tvbotconfig.Config, requestedAPIID string) (tvbotgate.Credentials, string, bool) {
	store := s.gateCredentialStore(cfg.Trading.Env)
	if store == nil {
		return tvbotgate.Credentials{}, "", false
	}
	status := store.Status()
	if !status.Configured {
		return tvbotgate.Credentials{}, strings.TrimSpace(status.ActiveID), false
	}
	creds, apiID, err := store.GateCredentials(requestedAPIID)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("failed to resolve Gate analysis credentials", "error", err)
		}
		return tvbotgate.Credentials{}, strings.TrimSpace(status.ActiveID), false
	}
	return creds, apiID, true
}

func (s *Server) analysisGateClient(cfg tvbotconfig.Config, creds tvbotgate.Credentials) tvbotgate.Client {
	return tvbotgate.Client{
		BaseURL:     cfg.GateBaseURL(),
		Credentials: creds,
		HTTPClient:  s.GateHTTPClient,
	}
}

func (s *Server) fetchGateAnalysisData(ctx context.Context, client tvbotgate.Client, apiID string, pnlMinutes int, now time.Time) ([]analysisTrade, []analysisPositionTrade, error) {
	since := analysisPositionSince(now, pnlMinutes)
	trades, tradeErr := fetchGateAnalysisTrades(ctx, client, since, now.UTC())
	positions, positionErr := fetchGateAnalysisPositionCloses(ctx, client, analysisPNLSince(now, pnlMinutes), now.UTC())

	convertedTrades := make([]analysisTrade, 0, len(trades))
	for _, trade := range trades {
		row, ok := analysisTradeFromGateTrade(apiID, trade)
		if ok {
			convertedTrades = append(convertedTrades, row)
		}
	}
	convertedPositions := make([]analysisPositionTrade, 0, len(positions))
	for _, position := range positions {
		row, ok := analysisPositionTradeFromGateClose(apiID, position, analysisPNLSince(now, pnlMinutes))
		if ok {
			convertedPositions = append(convertedPositions, row)
		}
	}
	return convertedTrades, convertedPositions, errors.Join(tradeErr, positionErr)
}

func fetchGateAnalysisTrades(ctx context.Context, client tvbotgate.Client, since, until time.Time) ([]tvbotgate.MyTrade, error) {
	out := []tvbotgate.MyTrade{}
	for page := 0; page < gateAnalysisMaxPages; page++ {
		offset := page * gateAnalysisPageLimit
		rows, err := client.MyTradesTimerange(ctx, "", since, until, gateAnalysisPageLimit, offset)
		if err != nil {
			return out, err
		}
		out = append(out, rows...)
		if len(rows) < gateAnalysisPageLimit {
			return out, nil
		}
	}
	return out, fmt.Errorf("Gate analysis trades reached %d page limit; analysis trade history may be incomplete", gateAnalysisMaxPages)
}

func fetchGateAnalysisPositionCloses(ctx context.Context, client tvbotgate.Client, since, until time.Time) ([]tvbotgate.PositionClose, error) {
	out := []tvbotgate.PositionClose{}
	for page := 0; page < gateAnalysisMaxPages; page++ {
		offset := page * gateAnalysisPageLimit
		rows, err := client.PositionCloses(ctx, "", since, until, gateAnalysisPageLimit, offset)
		if err != nil {
			return out, err
		}
		out = append(out, rows...)
		if len(rows) < gateAnalysisPageLimit {
			return out, nil
		}
	}
	return out, fmt.Errorf("Gate position closes reached %d page limit; analysis position history may be incomplete", gateAnalysisMaxPages)
}

func analysisTradeFromGateTrade(apiID string, trade tvbotgate.MyTrade) (analysisTrade, bool) {
	instID := strings.ToUpper(strings.TrimSpace(trade.Contract))
	if instID == "" {
		return analysisTrade{}, false
	}
	fillTime, ok := gateUnixTime(trade.CreateTime)
	if !ok {
		return analysisTrade{}, false
	}
	size, err := strconv.ParseFloat(strings.TrimSpace(trade.Size), 64)
	if err != nil || math.Abs(size) <= positionEntrySizeEpsilon {
		return analysisTrade{}, false
	}
	side := "buy"
	if size < 0 {
		side = "sell"
	}
	positionSide := ""
	positionEffect := ""
	if closeSize, err := strconv.ParseFloat(strings.TrimSpace(trade.CloseSize), 64); err == nil && math.Abs(closeSize) > positionEntrySizeEpsilon {
		positionEffect = tvbottrading.PositionEffectClose
		if closeSize > 0 {
			positionSide = tvbottrading.PositionSideShort
		} else {
			positionSide = tvbottrading.PositionSideLong
		}
	} else {
		positionEffect = tvbottrading.PositionEffectOpen
		if size > 0 {
			positionSide = tvbottrading.PositionSideLong
		} else {
			positionSide = tvbottrading.PositionSideShort
		}
	}
	return analysisTrade{
		Exchange:       tvbottrading.ExchangeGate,
		APIID:          strings.TrimSpace(apiID),
		InstID:         instID,
		TradeID:        strings.TrimSpace(trade.ID),
		OrdID:          strings.TrimSpace(trade.OrderID),
		Side:           side,
		PosSide:        positionSide,
		PositionEffect: positionEffect,
		FillPx:         strings.TrimSpace(trade.Price),
		FillSz:         strconv.FormatFloat(math.Abs(size), 'f', -1, 64),
		Turnover:       strings.TrimSpace(trade.TradeValue),
		Fee:            normalizedGateFee(trade.Fee),
		FeeCcy:         "USDT",
		FillTime:       fillTime,
		FillTS:         fillTime.UnixMilli(),
		FillCount:      1,
	}, true
}

func normalizedGateFee(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return raw
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		if strings.HasPrefix(raw, "-") {
			return raw
		}
		return "-" + raw
	}
	if value > 0 {
		value = -value
	}
	return tvbottrading.NormalizeFloat(value)
}

func analysisPositionTradeFromGateClose(apiID string, close tvbotgate.PositionClose, since time.Time) (analysisPositionTrade, bool) {
	instID := strings.ToUpper(strings.TrimSpace(close.Contract))
	if instID == "" {
		return analysisPositionTrade{}, false
	}
	exitTime, ok := gateUnixTime(close.Time)
	if !ok || exitTime.Before(since) {
		return analysisPositionTrade{}, false
	}
	entryTime, _ := gateUnixTime(close.FirstOpenTime)
	side := normalizeAnalysisPositionSide(close.Side)
	if side == "" {
		return analysisPositionTrade{}, false
	}
	entryPx, exitPx := strings.TrimSpace(close.LongPrice), strings.TrimSpace(close.ShortPrice)
	if side == tvbottrading.PositionSideShort {
		entryPx, exitPx = exitPx, entryPx
	}
	qty := strings.TrimSpace(close.MaxSize)
	if _, ok := parsePositiveFloat(qty); !ok {
		qty = strings.TrimSpace(close.AccumSize)
	}
	turnover := ""
	if quantity, quantityOK := parsePositiveFloat(qty); quantityOK {
		entry, entryOK := parsePositiveFloat(entryPx)
		exit, exitOK := parsePositiveFloat(exitPx)
		if entryOK && exitOK {
			turnover = tvbottrading.NormalizeFloat(quantity * (entry + exit))
		}
	}
	entryTS := int64(0)
	if !entryTime.IsZero() {
		entryTS = entryTime.UnixMilli()
	}
	return analysisPositionTrade{
		Exchange:    tvbottrading.ExchangeGate,
		APIID:       strings.TrimSpace(apiID),
		InstID:      instID,
		Side:        side,
		EntryTime:   entryTime,
		EntryTS:     entryTS,
		ExitTime:    exitTime,
		ExitTS:      exitTime.UnixMilli(),
		EntryPx:     entryPx,
		ExitPx:      exitPx,
		Qty:         qty,
		RealizedPnL: strings.TrimSpace(close.PnlPnl),
		Fee:         strings.TrimSpace(close.PnlFee),
		FeeCcy:      "USDT",
		NetPnL:      strings.TrimSpace(close.Pnl),
		Turnover:    turnover,
		FillCount:   1,
	}, true
}

func sortAnalysisPositionTrades(positions []analysisPositionTrade) {
	sort.SliceStable(positions, func(i, j int) bool {
		if positions[i].ExitTS == positions[j].ExitTS {
			if positions[i].Exchange == positions[j].Exchange {
				return positions[i].InstID < positions[j].InstID
			}
			return positions[i].Exchange < positions[j].Exchange
		}
		return positions[i].ExitTS > positions[j].ExitTS
	})
}
