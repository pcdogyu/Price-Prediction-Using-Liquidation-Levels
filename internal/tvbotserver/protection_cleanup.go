package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const protectionCleanupInterval = 120 * time.Second

// StartProtectionCleanupMonitor continuously removes exchange-side protective
// orders. It deliberately targets only TP/SL/trailing orders and close/reduce
// only Gate trigger orders, leaving ordinary entry and close orders intact.
func (s *Server) StartProtectionCleanupMonitor(ctx context.Context) {
	if s == nil || s.ConfigStore == nil {
		return
	}
	go func() {
		s.cleanupProtectionOrders(ctx, s.ConfigStore.Get())
		ticker := time.NewTicker(protectionCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanupProtectionOrders(ctx, s.ConfigStore.Get())
			}
		}
	}()
}

func (s *Server) cleanupProtectionOrders(ctx context.Context, cfg tvbotconfig.Config) {
	if cfg.Trading.ProtectionOrdersEnabled {
		return
	}
	for _, env := range []string{tvbotconfig.EnvDemo, tvbotconfig.EnvLive} {
		envCfg := cfg
		envCfg.Trading.Env = env
		s.cleanupProtectionOrdersForEnv(ctx, envCfg)
	}
}

func (s *Server) cleanupProtectionOrdersForEnv(ctx context.Context, cfg tvbotconfig.Config) {
	primary := s.primaryExchangeForConfig(cfg)
	if primary == tvbottrading.ExchangeOKX {
		if store := s.okxCredentialStore(cfg.Trading.Env); store != nil {
			for _, apiID := range configuredAPIIDs(store.Status()) {
				cleanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
				if err := s.cleanupOKXProtectionOrders(cleanCtx, cfg, apiID); err != nil && s.Logger != nil {
					s.Logger.Warn("failed to clean OKX protection orders", "api_id", apiID, "error", err)
				}
				cancel()
			}
		}
	}
	if primary == tvbottrading.ExchangeBinance {
		if store := s.binanceCredentialStore(cfg.Trading.Env); store != nil {
			for _, apiID := range configuredBinanceAPIIDs(store.Status()) {
				cleanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
				if err := s.cleanupBinanceProtectionOrders(cleanCtx, cfg, apiID); err != nil && s.Logger != nil {
					s.Logger.Warn("failed to clean Binance protection orders", "api_id", apiID, "error", err)
				}
				cancel()
			}
		}
	}
	if primary == tvbottrading.ExchangeGate {
		if store := s.gateCredentialStore(cfg.Trading.Env); store != nil {
			for _, apiID := range configuredGateAPIIDs(store.Status()) {
				cleanCtx, cancel := context.WithTimeout(ctx, positionMonitorScanTimeout)
				if err := s.cleanupGateProtectionOrders(cleanCtx, cfg, apiID); err != nil && s.Logger != nil {
					s.Logger.Warn("failed to clean Gate protection orders", "api_id", apiID, "error", err)
				}
				cancel()
			}
		}
	}

}

func (s *Server) cleanupOKXProtectionOrders(ctx context.Context, cfg tvbotconfig.Config, requestedAPIID string) error {
	client, _, err := s.okxClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	orders, _, err := client.PendingAlgoOrders(ctx, "SWAP", "")
	if err != nil {
		return err
	}
	var reqs []tvbotokx.CancelAlgoOrderRequest
	for _, order := range orders {
		if !isOKXProtectionOrder(order) {
			continue
		}
		req := tvbotokx.CancelAlgoOrderRequest{InstID: strings.TrimSpace(order.InstID), AlgoID: strings.TrimSpace(order.AlgoID), AlgoClOrdID: strings.TrimSpace(order.AlgoClOrdID)}
		if req.InstID == "" || (req.AlgoID == "" && req.AlgoClOrdID == "") {
			continue
		}
		reqs = append(reqs, req)
	}
	for len(reqs) > 0 {
		n := min(len(reqs), 10)
		if _, _, err := client.CancelAlgoOrders(ctx, reqs[:n]); err != nil {
			return err
		}
		reqs = reqs[n:]
	}
	pending, _, err := client.PendingOrders(ctx, "SWAP")
	if err != nil {
		return err
	}
	for _, order := range pending {
		if len(order.AttachAlgoOrds) == 0 {
			continue
		}
		req := tvbotokx.AmendOrderRequest{InstID: order.InstID, OrdID: order.OrdID, ClOrdID: order.ClOrdID, NewPx: order.Px, NewSz: order.Sz, ClearAttachAlgoOrds: true}
		if _, _, err := client.AmendOrder(ctx, req); err != nil {
			return err
		}
	}
	return nil
}

func isOKXProtectionOrder(order tvbotokx.AlgoOrder) bool {
	return tvbotokxProtectionPriceSet(order.TPTriggerPx) || tvbotokxProtectionPriceSet(order.SLTriggerPx) || strings.EqualFold(strings.TrimSpace(order.OrdType), "move_order_stop") || strings.TrimSpace(order.CallbackRatio) != "" || strings.TrimSpace(order.CallbackSpread) != ""
}

func tvbotokxProtectionPriceSet(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "-1"
}

func (s *Server) cleanupBinanceProtectionOrders(ctx context.Context, cfg tvbotconfig.Config, requestedAPIID string) error {
	client, _, err := s.binanceClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	algos, err := client.OpenAlgoOrders(ctx, "")
	if err != nil {
		return err
	}
	for _, order := range algos {
		if !isBinanceProtectionOrder(order) {
			continue
		}
		if _, err := client.CancelAlgoOrder(ctx, order.AlgoID, order.ClientAlgoID); err != nil {
			return err
		}
	}
	orders, err := client.OpenOrders(ctx, "")
	if err != nil {
		return err
	}
	for _, order := range orders {
		if !isBinanceLegacyProtectionOrder(order) {
			continue
		}
		if _, err := client.CancelOrder(ctx, tvbotbinance.CancelOrderRequest{Symbol: order.Symbol, OrderID: strconv.FormatInt(order.OrderID, 10), OrigClientOrderID: order.ClientOrderID}); err != nil {
			return err
		}
	}
	return nil
}

func isBinanceProtectionOrder(order tvbotbinance.AlgoOpenOrder) bool {
	// Binance also uses conditional orders for ordinary stop entries. Only
	// close-position/reduce-only orders are unambiguously position protection.
	return order.ClosePosition || order.ReduceOnly
}

func isBinanceLegacyProtectionOrder(order tvbotbinance.OpenOrder) bool {
	if order.ClosePosition {
		return true
	}
	if !order.ReduceOnly {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(order.Type)) {
	case "STOP", "STOP_MARKET", "TAKE_PROFIT", "TAKE_PROFIT_MARKET", "TRAILING_STOP_MARKET":
		return true
	default:
		// A regular reduce-only LIMIT/MARKET order is an ordinary close and must
		// survive the protection sweep.
		return false
	}
}

func (s *Server) cleanupGateProtectionOrders(ctx context.Context, cfg tvbotconfig.Config, requestedAPIID string) error {
	client, _, err := s.gateClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	orders, err := client.OpenPriceTriggeredOrders(ctx, "")
	if err != nil {
		return err
	}
	for _, order := range orders {
		if !gateProtectionOrder(order) {
			continue
		}
		if err := cancelGatePriceTriggeredOrder(ctx, client, order); err != nil {
			return fmt.Errorf("cancel Gate protection %s: %w", order.IDString(), err)
		}
	}
	return nil
}

func gateProtectionOrder(order tvbotgate.PriceTriggeredOrder) bool {
	return gatePriceOrderIsClose(order) || gatePriceOrderIsReduceOnly(order) || strings.HasPrefix(strings.TrimSpace(order.Initial.Text), gatePositionRiskTextPrefix)
}
