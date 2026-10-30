package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

const (
	gatePositionRiskPollInterval = 10 * time.Second
	gatePositionRiskPollTimeout  = 8 * time.Second
	gatePositionRiskTextPrefix   = "t-rk-"
)

type gateRiskSpec struct {
	record tvbotstorage.OrderRecord
}

type gateRiskPlan struct {
	contract   string
	kind       string
	orderType  string
	triggerPx  string
	rule       int64
	clientText string
	resultType string
	closeSide  string
	marginMode string
}

// StartGatePositionRiskMonitor keeps Gate TP/SL orders synchronized with the
// actual net position average price. It is intentionally separate from the
// generic position scanner: this monitor only manages risk for orders created
// by the post-fill Gate path.
func (s *Server) StartGatePositionRiskMonitor(ctx context.Context) {
	if s == nil || s.ConfigStore == nil || s.Orders == nil || (s.GateCredentials == nil && s.GateLiveCredentials == nil) {
		return
	}
	go s.runGatePositionRiskMonitor(ctx)
}

func (s *Server) runGatePositionRiskMonitor(ctx context.Context) {
	s.reconcileGatePositionRisk(ctx)
	ticker := time.NewTicker(gatePositionRiskPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileGatePositionRisk(ctx)
		}
	}
}

func (s *Server) reconcileGatePositionRisk(ctx context.Context) {
	if s == nil || s.ConfigStore == nil || s.Orders == nil {
		return
	}
	records := s.Orders.ListByTargetExchange(tvbottrading.ExchangeGate, 500)
	specs := make(map[string]gateRiskSpec)
	for _, record := range records {
		if !gatePostFillRiskRecord(record) {
			continue
		}
		contract := strings.TrimSpace(record.Result.InstID)
		if contract == "" {
			continue
		}
		apiID := strings.TrimSpace(record.Result.APIID)
		if apiID == "" {
			apiID = strings.TrimSpace(record.APIID)
		}
		key := strings.Join([]string{
			normalizeGateRiskEnvironment(record.TradeEnv),
			apiID,
			strings.ToUpper(contract),
		}, "|")
		// ListByTargetExchange is newest-first. The newest order owns the
		// current risk settings for a net Gate position.
		if _, exists := specs[key]; !exists {
			specs[key] = gateRiskSpec{record: record}
		}
	}
	for _, spec := range specs {
		pollCtx, cancel := context.WithTimeout(ctx, gatePositionRiskPollTimeout)
		err := s.reconcileGatePositionRiskForRecord(pollCtx, spec.record)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to reconcile Gate position TP/SL", "signal_id", spec.record.SignalID, "contract", spec.record.Result.InstID, "error", err)
		}
	}
}

func (s *Server) reconcileGatePositionRiskForSignal(signalID string) {
	if s == nil || s.Orders == nil {
		return
	}
	record, ok := s.Orders.Get(signalID)
	if !ok || !gatePostFillRiskRecord(record) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatePositionRiskPollTimeout)
	defer cancel()
	if err := s.reconcileGatePositionRiskForRecord(ctx, record); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to reconcile Gate position TP/SL after order submission", "signal_id", signalID, "contract", record.Result.InstID, "error", err)
	}
}

func gatePostFillRiskRecord(record tvbotstorage.OrderRecord) bool {
	if record.Status != tvbotstorage.StatusSubmitted && record.Status != tvbotstorage.StatusPartial {
		return false
	}
	if tvbottrading.NormalizeExchange(record.TargetExchange) != tvbottrading.ExchangeGate && tvbottrading.NormalizeExchange(record.Result.TargetExchange) != tvbottrading.ExchangeGate {
		return false
	}
	if strings.TrimSpace(record.PositionEffect) != tvbottrading.PositionEffectOpen {
		return false
	}
	if record.Risk.Type != tvbottrading.RiskTPSL || record.Risk.TPPct == nil || record.Risk.SLPct == nil {
		return false
	}
	return strings.TrimSpace(record.Result.RiskReference) == tvbottrading.RiskReferencePositionAverage
}

func (s *Server) reconcileGatePositionRiskForRecord(ctx context.Context, record tvbotstorage.OrderRecord) error {
	if !gatePostFillRiskRecord(record) {
		return nil
	}
	cfg := configForTradeEnv(s.ConfigStore.Get(), orderRecordTradeEnv(record))
	requestedAPIID := strings.TrimSpace(record.Result.APIID)
	if requestedAPIID == "" {
		requestedAPIID = strings.TrimSpace(record.APIID)
	}
	client, _, err := s.gateClientForCredentials(cfg, requestedAPIID)
	if err != nil {
		return err
	}
	contract := strings.TrimSpace(record.Result.InstID)
	if contract == "" {
		return errors.New("Gate risk reconciliation requires a contract")
	}
	position, found, err := openGatePosition(ctx, client, contract)
	if err != nil {
		return err
	}
	openRiskOrders, err := client.OpenPriceTriggeredOrders(ctx, contract)
	if err != nil {
		return err
	}
	if !found {
		return cancelManagedGateRiskOrders(ctx, client, contract, openRiskOrders)
	}
	contractMeta, err := client.Contract(ctx, contract)
	if err != nil {
		return err
	}
	plan, err := gatePositionRiskPlan(record, position, contractMeta.OrderPriceRound, requestedAPIID, gateRiskMarginMode(cfg.Trading.DefaultMarginMode))
	if err != nil {
		return err
	}
	managed := managedGateRiskOrders(openRiskOrders, plan)
	result := record.Result
	result.RiskOrders = result.RiskOrders[:0]
	var reconcileErr error
	for _, desired := range plan {
		orders := managed[desired.clientText]
		orderID, err := s.ensureGateRiskOrder(ctx, client, desired, orders)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		result.RiskOrders = append(result.RiskOrders, tvbottrading.RiskOrderResult{
			Exchange:     tvbottrading.ExchangeGate,
			AlgoID:       orderID,
			ClientAlgoID: desired.clientText,
			OrderType:    desired.resultType,
			Side:         desired.closeSide,
			Quantity:     "0",
			TriggerPrice: desired.triggerPx,
		})
	}
	if len(result.RiskOrders) > 0 {
		if err := s.Orders.UpdateResult(record.SignalID, result, s.now()); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func openGatePosition(ctx context.Context, client tvbotgate.Client, contract string) (tvbotgate.Position, bool, error) {
	positions, err := client.Positions(ctx, contract)
	if err != nil {
		return tvbotgate.Position{}, false, err
	}
	for _, position := range positions {
		if !strings.EqualFold(strings.TrimSpace(position.Contract), strings.TrimSpace(contract)) {
			continue
		}
		size, parseErr := strconv.ParseFloat(strings.TrimSpace(position.Size), 64)
		if parseErr == nil && math.Abs(size) > 0 {
			return position, true, nil
		}
	}
	return tvbotgate.Position{}, false, nil
}

func gatePositionRiskPlan(record tvbotstorage.OrderRecord, position tvbotgate.Position, priceStep, apiID, marginMode string) ([]gateRiskPlan, error) {
	entry, err := strconv.ParseFloat(strings.TrimSpace(position.EntryPrice), 64)
	if err != nil || entry <= 0 {
		return nil, fmt.Errorf("invalid Gate position entry price %q", position.EntryPrice)
	}
	size, err := strconv.ParseFloat(strings.TrimSpace(position.Size), 64)
	if err != nil || size == 0 {
		return nil, fmt.Errorf("invalid Gate position size %q", position.Size)
	}
	leverage := gatePositionLeverage(position, record.Result.Leverage, record.Leverage)
	if leverage <= 0 {
		return nil, errors.New("Gate position leverage is unavailable")
	}
	tpPct := record.Risk.TPPct.Value
	slPct := record.Risk.SLPct.Value
	action := tvbottrading.ActionLong
	if size < 0 {
		action = tvbottrading.ActionShort
	}
	tp, sl, err := tvbottrading.FixedTPSLTriggerPrices(action, entry, tpPct, slPct, leverage)
	if err != nil {
		return nil, err
	}
	long := action == tvbottrading.ActionLong
	orderType := "close-long-order"
	closeSide := "SELL"
	if !long {
		orderType = "close-short-order"
		closeSide = "BUY"
	}
	tpRule, slRule := int64(1), int64(2)
	if !long {
		tpRule, slRule = 2, 1
	}
	contract := strings.ToUpper(strings.TrimSpace(position.Contract))
	return []gateRiskPlan{
		{contract: contract, kind: "tp", orderType: orderType, triggerPx: tvbotgate.FormatPriceStep(tp, priceStep), rule: tpRule, clientText: gateRiskClientText(apiID, contract, "tp"), resultType: "TAKE_PROFIT", closeSide: closeSide, marginMode: marginMode},
		{contract: contract, kind: "sl", orderType: orderType, triggerPx: tvbotgate.FormatPriceStep(sl, priceStep), rule: slRule, clientText: gateRiskClientText(apiID, contract, "sl"), resultType: "STOP_LOSS", closeSide: closeSide, marginMode: marginMode},
	}, nil
}

func gatePositionLeverage(position tvbotgate.Position, fallbacks ...int) int {
	for _, raw := range []string{position.Lever, position.Leverage} {
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err == nil && value > 0 && math.Trunc(value) == value {
			return int(value)
		}
	}
	for _, fallback := range fallbacks {
		if fallback > 0 {
			return fallback
		}
	}
	return 0
}

func managedGateRiskOrders(orders []tvbotgate.PriceTriggeredOrder, plans []gateRiskPlan) map[string][]tvbotgate.PriceTriggeredOrder {
	managed := make(map[string][]tvbotgate.PriceTriggeredOrder, len(plans))
	for _, plan := range plans {
		managed[plan.clientText] = nil
	}
	for _, order := range orders {
		if _, ok := managed[order.Initial.Text]; ok {
			managed[order.Initial.Text] = append(managed[order.Initial.Text], order)
		}
	}
	return managed
}

func (s *Server) ensureGateRiskOrder(ctx context.Context, client tvbotgate.Client, desired gateRiskPlan, existing []tvbotgate.PriceTriggeredOrder) (string, error) {
	if len(existing) > 0 {
		keep := existing[0]
		for _, duplicate := range existing[1:] {
			if err := cancelGatePriceTriggeredOrder(ctx, client, duplicate); err != nil && s.Logger != nil {
				s.Logger.Warn("failed to cancel duplicate Gate TP/SL order", "order_id", duplicate.IDString(), "error", err)
			}
		}
		if keep.OrderType != desired.orderType || !gatePriceOrderIsClose(keep) || !gatePriceOrderIsReduceOnly(keep) {
			if err := cancelGatePriceTriggeredOrder(ctx, client, keep); err != nil {
				return "", fmt.Errorf("replace Gate %s order %s: %w", desired.kind, keep.IDString(), err)
			}
			return s.ensureGateRiskOrder(ctx, client, desired, nil)
		}
		if keep.OrderType == desired.orderType && keep.Trigger.Price == desired.triggerPx && keep.Trigger.Rule == desired.rule && gatePriceOrderIsClose(keep) && gatePriceOrderIsReduceOnly(keep) {
			return keep.IDString(), nil
		}
		response, err := client.AmendPriceTriggeredOrder(ctx, keep.IDString(), tvbotgate.PriceTriggeredOrderAmendment{
			OrderID:   keep.IDString(),
			Size:      "0",
			Price:     "0",
			TriggerPx: desired.triggerPx,
			PriceType: 0,
			Close:     true,
		})
		if err != nil {
			return "", fmt.Errorf("amend Gate %s order %s: %w", desired.kind, keep.IDString(), err)
		}
		if id := response.IDValue(); id != "" {
			return id, nil
		}
		return keep.IDString(), nil
	}
	response, err := client.PlacePriceTriggeredOrder(ctx, tvbotgate.PriceTriggeredOrderRequest{
		Initial: tvbotgate.PriceTriggeredInitial{
			Contract:   desired.contract,
			Size:       "0",
			Price:      "0",
			TIF:        "ioc",
			Text:       desired.clientText,
			Close:      true,
			ReduceOnly: true,
		},
		Trigger: tvbotgate.PriceTriggeredTrigger{
			StrategyType: 0,
			PriceType:    0,
			Price:        desired.triggerPx,
			Rule:         desired.rule,
		},
		OrderType:     desired.orderType,
		PosMarginMode: desired.marginMode,
	})
	if err != nil {
		return "", fmt.Errorf("create Gate %s order: %w", desired.kind, err)
	}
	if id := response.IDValue(); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("create Gate %s order returned no order id", desired.kind)
}

func cancelManagedGateRiskOrders(ctx context.Context, client tvbotgate.Client, contract string, orders []tvbotgate.PriceTriggeredOrder) error {
	var cancelErr error
	for _, order := range orders {
		if !strings.HasPrefix(order.Initial.Text, gatePositionRiskTextPrefix) {
			continue
		}
		if err := cancelGatePriceTriggeredOrder(ctx, client, order); err != nil {
			cancelErr = errors.Join(cancelErr, fmt.Errorf("cancel Gate risk order %s: %w", order.IDString(), err))
		}
	}
	return cancelErr
}

func cancelGatePriceTriggeredOrder(ctx context.Context, client tvbotgate.Client, order tvbotgate.PriceTriggeredOrder) error {
	if strings.TrimSpace(order.IDString()) == "" {
		return errors.New("Gate risk order has no id")
	}
	_, err := client.CancelPriceTriggeredOrder(ctx, order.IDString())
	return err
}

func gatePriceOrderIsClose(order tvbotgate.PriceTriggeredOrder) bool {
	return order.Initial.Close || order.Initial.IsClose
}

func gatePriceOrderIsReduceOnly(order tvbotgate.PriceTriggeredOrder) bool {
	return order.Initial.ReduceOnly || order.Initial.IsReduceOnly
}

func gateRiskClientText(apiID, contract, kind string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{apiID, contract, kind}, "|")))
	return gatePositionRiskTextPrefix + kind + "-" + hex.EncodeToString(digest[:])[:12]
}

func normalizeGateRiskEnvironment(raw string) string {
	if tvbottrading.NormalizeTradeEnv(raw) == tvbottrading.TradeEnvLive {
		return tvbottrading.TradeEnvLive
	}
	return tvbottrading.TradeEnvDemo
}

func gateRiskMarginMode(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "cross") {
		return "cross"
	}
	return "isolated"
}
