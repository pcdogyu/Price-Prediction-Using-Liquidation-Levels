package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const scheduledPositionCloseScanTimeout = 75 * time.Second

// RunScheduledPositionClose performs one full scheduled close pass for the
// current tenant. It deliberately ignores the per-exchange price-monitor
// toggles: the daily close is an independent full-account safety rule.
func (s *Server) RunScheduledPositionClose(ctx context.Context) error {
	if s == nil || s.ConfigStore == nil {
		return errors.New("TVBot config store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := s.ConfigStore.Get()
	if !cfg.Trading.PositionMonitor.DailyCloseEnabled {
		return nil
	}

	var errs []error
	if err := s.runScheduledOKXClose(ctx, cfg); err != nil {
		errs = append(errs, err)
	}
	if err := s.runScheduledBinanceClose(ctx, cfg); err != nil {
		errs = append(errs, err)
	}
	if err := s.runScheduledGateClose(ctx, cfg); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Server) runScheduledOKXClose(ctx context.Context, cfg tvbotconfig.Config) error {
	store := s.okxCredentialStore(cfg.Trading.Env)
	if store == nil || !cfg.LiveTradingAllowedByEnvironment() {
		return nil
	}
	var errs []error
	for _, requestedAPIID := range configuredAPIIDs(store.Status()) {
		closeCtx, cancel := context.WithTimeout(ctx, scheduledPositionCloseScanTimeout)
		err := s.runScheduledOKXCloseForAPI(closeCtx, cfg, requestedAPIID)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("OKX api %q: %w", requestedAPIID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Server) runScheduledOKXCloseForAPI(ctx context.Context, cfg tvbotconfig.Config, requestedAPIID string) error {
	client, apiID, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, _, err := client.Positions(ctx, "SWAP")
	if err != nil {
		return err
	}
	var errs []error
	for _, position := range openPositions(positions) {
		order, started, closeErr := s.startManualLimitPositionClose(ctx, apiID, cfg, client, position, "")
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", position.InstID, position.PosSide, closeErr))
			continue
		}
		if started && s.Logger != nil {
			s.Logger.Info("scheduled OKX position close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "ord_id", order.Ack.OrdID)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) runScheduledBinanceClose(ctx context.Context, cfg tvbotconfig.Config) error {
	store := s.binanceCredentialStore(cfg.Trading.Env)
	if store == nil || !cfg.BinanceLiveTradingAllowedByEnvironment() {
		return nil
	}
	var errs []error
	for _, requestedAPIID := range configuredBinanceAPIIDs(store.Status()) {
		closeCtx, cancel := context.WithTimeout(ctx, scheduledPositionCloseScanTimeout)
		err := s.runScheduledBinanceCloseForAPI(closeCtx, cfg, requestedAPIID)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("Binance api %q: %w", requestedAPIID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Server) runScheduledBinanceCloseForAPI(ctx context.Context, cfg tvbotconfig.Config, requestedAPIID string) error {
	client, apiID, err := s.binanceClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	client = binancePositionCloseClient(client)
	positions, err := client.Positions(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, raw := range positions {
		position := binancePositionToOKX(raw)
		if !isOpenPosition(position.Pos) {
			continue
		}
		order, started, closeErr := s.startScheduledBinanceLimitPositionClose(ctx, apiID, client, position)
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", position.InstID, position.PosSide, closeErr))
			continue
		}
		if started && s.Logger != nil {
			s.Logger.Info("scheduled Binance position close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "ord_id", order.Ack.OrdID)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) startScheduledBinanceLimitPositionClose(ctx context.Context, apiID string, client tvbotbinance.Client, position tvbotokx.Position) (positionCloseOrder, bool, error) {
	key := positionCloseKey(tvbottrading.ExchangeBinance, apiID, position.InstID, position.PosSide)
	if !positionCloseJobs.start(key) {
		return positionCloseOrder{}, false, nil
	}
	if err := cancelAllBinanceSymbolOrders(ctx, client, position.InstID); err != nil {
		positionCloseJobs.done(key)
		return positionCloseOrder{}, false, err
	}
	order, err := placeBinancePositionClose(ctx, client, position, "limit", "")
	if err != nil {
		positionCloseJobs.done(key)
		return positionCloseOrder{}, false, err
	}
	go s.watchBinanceLimitPositionClose(apiID, client, order)
	return order, true, nil
}

func (s *Server) runScheduledGateClose(ctx context.Context, cfg tvbotconfig.Config) error {
	store := s.gateCredentialStore(cfg.Trading.Env)
	if store == nil || !cfg.GateLiveTradingAllowedByEnvironment() {
		return nil
	}
	var errs []error
	for _, requestedAPIID := range configuredGateAPIIDs(store.Status()) {
		closeCtx, cancel := context.WithTimeout(ctx, scheduledPositionCloseScanTimeout)
		err := s.runScheduledGateCloseForAPI(closeCtx, cfg, requestedAPIID)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("Gate api %q: %w", requestedAPIID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Server) runScheduledGateCloseForAPI(ctx context.Context, cfg tvbotconfig.Config, requestedAPIID string) error {
	client, apiID, err := s.gateClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	positions, err := client.Positions(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, raw := range positions {
		position := gatePositionToOKX(raw)
		if !isOpenPosition(position.Pos) {
			continue
		}
		order, started, closeErr := s.startScheduledGateLimitPositionClose(ctx, apiID, cfg, client, position)
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", position.InstID, position.PosSide, closeErr))
			continue
		}
		if started && s.Logger != nil {
			s.Logger.Info("scheduled Gate position close started", "api_id", apiID, "inst_id", position.InstID, "pos_side", position.PosSide, "ord_id", order.Order.IDString())
		}
	}
	return errors.Join(errs...)
}

func (s *Server) startScheduledGateLimitPositionClose(ctx context.Context, apiID string, cfg tvbotconfig.Config, client tvbotgate.Client, position tvbotokx.Position) (gatePositionCloseOrder, bool, error) {
	key := positionCloseKey(tvbottrading.ExchangeGate, apiID, position.InstID, position.PosSide)
	if !positionCloseJobs.start(key) {
		return gatePositionCloseOrder{}, false, nil
	}
	if err := cancelAllGateContractOrders(ctx, client, position.InstID); err != nil {
		positionCloseJobs.done(key)
		return gatePositionCloseOrder{}, false, err
	}
	order, err := placeGateLimitPositionClose(ctx, cfg, client, position, "")
	if err != nil {
		positionCloseJobs.done(key)
		return gatePositionCloseOrder{}, false, err
	}
	go s.watchGateLimitPositionClose(apiID, cfg, client, order)
	return order, true, nil
}
