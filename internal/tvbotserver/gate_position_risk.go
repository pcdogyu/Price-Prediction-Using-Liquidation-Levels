package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
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
	size       string
	amount     string
	quantity   string
}

type gateLayerFill struct {
	quantity  float64
	price     float64
	orderSize float64
}

type gateRiskReconcileRegistry struct {
	mu   sync.Mutex
	jobs map[string]struct{}
}

func newGateRiskReconcileRegistry() *gateRiskReconcileRegistry {
	return &gateRiskReconcileRegistry{jobs: make(map[string]struct{})}
}

func (r *gateRiskReconcileRegistry) start(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.jobs[key]; exists {
		return false
	}
	r.jobs[key] = struct{}{}
	return true
}

func (r *gateRiskReconcileRegistry) done(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, key)
}

var gateRiskReconcileJobs = newGateRiskReconcileRegistry()

type gateContractOperationRegistry struct {
	mu   sync.Mutex
	jobs map[string]struct{}
}

func newGateContractOperationRegistry() *gateContractOperationRegistry {
	return &gateContractOperationRegistry{jobs: make(map[string]struct{})}
}

func (r *gateContractOperationRegistry) start(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.jobs[key]; exists {
		return false
	}
	r.jobs[key] = struct{}{}
	return true
}

func (r *gateContractOperationRegistry) done(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, key)
}

var gateContractOperationJobs = newGateContractOperationRegistry()

// StartGatePositionRiskMonitor keeps one exchange-side TP/SL pair per filled
// Gate entry layer. The generic position monitor is intentionally separate and
// remains active: it is the page/configured scanner's market-close fallback.
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
	// Do not select only the newest order for a contract. Net-mode Gate
	// positions can contain several independently priced entry layers, and each
	// submitted layer owns its own protection pair.
	for _, record := range s.Orders.ListByTargetExchange(tvbottrading.ExchangeGate, 500) {
		if !gatePostFillRiskRecord(record) {
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, gatePositionRiskPollTimeout)
		err := s.reconcileGatePositionRiskForRecord(pollCtx, record)
		cancel()
		if err != nil && s.Logger != nil {
			s.Logger.Warn("failed to reconcile Gate layered TP/SL", "signal_id", record.SignalID, "contract", record.Result.InstID, "error", err)
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
		s.Logger.Warn("failed to reconcile Gate layered TP/SL after order submission", "signal_id", signalID, "contract", record.Result.InstID, "error", err)
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
	// Accept position_average for records written by the previous deployment;
	// they are migrated to per-layer protection on the next reconciliation.
	ref := strings.TrimSpace(record.Result.RiskReference)
	return ref == tvbottrading.RiskReferenceLayerAverage || ref == tvbottrading.RiskReferencePositionAverage
}

func (s *Server) reconcileGatePositionRiskForRecord(ctx context.Context, record tvbotstorage.OrderRecord) error {
	if !gatePostFillRiskRecord(record) {
		return nil
	}
	key := gateRiskReconcileKey(record)
	if !gateRiskReconcileJobs.start(key) {
		return nil
	}
	defer gateRiskReconcileJobs.done(key)
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
	operationKey := gateContractOperationKey(requestedAPIID, contract)
	if !gateContractOperationJobs.start(operationKey) {
		// The page position scanner is currently canceling/submitting a full
		// close, or another record is synchronizing this contract. Let that
		// operation finish before creating/amending exchange protection.
		return nil
	}
	defer gateContractOperationJobs.done(operationKey)
	_, found, err := openGatePosition(ctx, client, contract)
	if err != nil {
		return err
	}
	openRiskOrders, err := client.OpenPriceTriggeredOrders(ctx, contract)
	if err != nil {
		return err
	}
	if !found {
		if err := cancelManagedGateRiskOrders(ctx, client, contract, openRiskOrders); err != nil {
			return err
		}
		result := record.Result
		if clearGateRiskResult(&result) && !reflect.DeepEqual(result, record.Result) {
			return s.Orders.UpdateResult(record.SignalID, result, s.now())
		}
		return nil
	}
	contractMeta, err := client.Contract(ctx, contract)
	if err != nil {
		return err
	}
	marginMode := gateRiskMarginMode(cfg.Trading.DefaultMarginMode)
	result := record.Result
	ownedTexts := gateOwnedRiskTexts(record.Result, requestedAPIID, contract)
	if len(result.Layers) == 0 && strings.TrimSpace(result.OrdID) != "" {
		// Simple market/limit Gate opens predate layer metadata. Treat the
		// single order as one layer so it receives the same fill-based handling.
		result.Layers = []tvbottrading.OrderLayerResult{{
			Name:      "default",
			OrderType: result.OrdType,
			Price:     result.Px,
			OrdID:     strings.TrimSpace(result.OrdID),
			ClOrdID:   strings.TrimSpace(result.ClOrdID),
			Status:    "submitted",
		}}
	}
	result.RiskOrders = nil
	desiredTexts := make(map[string]struct{})
	var reconcileErr error
	for index := range result.Layers {
		layer := &result.Layers[index]
		layer.RiskOrders = nil
		if strings.TrimSpace(layer.OrdID) == "" {
			continue
		}
		fill, filled, fillErr := fetchGateLayerFill(ctx, client, contract, *layer)
		if fillErr != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("layer %s fill lookup: %w", layer.Name, fillErr))
			continue
		}
		if !filled {
			continue
		}
		layer.FilledQty = tvbottrading.NormalizeFloat(fill.quantity)
		layer.FillPrice = tvbottrading.NormalizeFloat(fill.price)
		plans, planErr := gateLayerRiskPlans(record, *layer, index, fill, contractMeta.OrderPriceRound, contractMeta.EnableDecimal, requestedAPIID, marginMode)
		if planErr != nil {
			reconcileErr = errors.Join(reconcileErr, planErr)
			continue
		}
		for _, desired := range plans {
			desiredTexts[desired.clientText] = struct{}{}
			managed := managedGateRiskOrdersForText(openRiskOrders, desired.clientText)
			orderID, ensureErr := s.ensureGateRiskOrder(ctx, client, desired, managed)
			if ensureErr != nil {
				reconcileErr = errors.Join(reconcileErr, ensureErr)
				continue
			}
			risk := gateRiskOrderResult(desired, orderID)
			layer.RiskOrders = append(layer.RiskOrders, risk)
			result.RiskOrders = append(result.RiskOrders, risk)
		}
	}
	// Remove protection left behind by an older layer plan, a canceled/empty
	// layer, or the previous net-position implementation. Never touch other
	// Gate price orders, including orders owned by the page position scanner.
	for _, order := range openRiskOrders {
		text := strings.TrimSpace(order.Initial.Text)
		if !strings.HasPrefix(text, gatePositionRiskTextPrefix) {
			continue
		}
		if _, owned := ownedTexts[text]; !owned {
			continue
		}
		if _, keep := desiredTexts[text]; keep {
			continue
		}
		if cancelErr := cancelGatePriceTriggeredOrder(ctx, client, order); cancelErr != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("cancel stale Gate risk order %s: %w", order.IDString(), cancelErr))
		}
	}
	if !reflect.DeepEqual(result, record.Result) {
		if updateErr := s.Orders.UpdateResult(record.SignalID, result, s.now()); updateErr != nil {
			reconcileErr = errors.Join(reconcileErr, updateErr)
		}
	}
	return reconcileErr
}

func gateContractOperationKey(apiID, contract string) string {
	return strings.Join([]string{strings.TrimSpace(apiID), strings.ToUpper(strings.TrimSpace(contract))}, "|")
}

func gateRiskReconcileKey(record tvbotstorage.OrderRecord) string {
	key := strings.TrimSpace(record.SignalID)
	if key == "" {
		key = strings.TrimSpace(record.Result.OrdID)
	}
	return strings.Join([]string{normalizeGateRiskEnvironment(record.TradeEnv), strings.TrimSpace(record.APIID), strings.TrimSpace(record.Result.InstID), key}, "|")
}

func gateOwnedRiskTexts(result tvbottrading.OrderResult, apiID, contract string) map[string]struct{} {
	owned := make(map[string]struct{})
	for _, risk := range result.RiskOrders {
		if text := strings.TrimSpace(risk.ClientAlgoID); text != "" {
			owned[text] = struct{}{}
		}
	}
	for _, layer := range result.Layers {
		for _, risk := range layer.RiskOrders {
			if text := strings.TrimSpace(risk.ClientAlgoID); text != "" {
				owned[text] = struct{}{}
			}
		}
	}
	// The previous deployment used these two net-position IDs. Include them
	// only as this record's migration targets; never sweep another record's
	// per-layer protection orders.
	owned[gateRiskClientText(apiID, contract, "tp")] = struct{}{}
	owned[gateRiskClientText(apiID, contract, "sl")] = struct{}{}
	return owned
}

func fetchGateLayerFill(ctx context.Context, client tvbotgate.Client, contract string, layer tvbottrading.OrderLayerResult) (gateLayerFill, bool, error) {
	orderID := strings.TrimSpace(layer.OrdID)
	if orderID == "" {
		return gateLayerFill{}, false, nil
	}
	order, err := client.GetOrder(ctx, contract, orderID)
	if err != nil {
		if persisted, ok := persistedGateLayerFill(layer); ok {
			return persisted, true, nil
		}
		return gateLayerFill{}, false, err
	}
	total, totalErr := strconv.ParseFloat(strings.TrimSpace(order.Size), 64)
	left, leftErr := strconv.ParseFloat(strings.TrimSpace(order.Left), 64)
	filledQty := 0.0
	if totalErr == nil {
		filledQty = math.Abs(total)
		if leftErr == nil {
			filledQty -= math.Abs(left)
		}
		if filledQty < 0 {
			filledQty = 0
		}
	}
	fillPrice, priceErr := strconv.ParseFloat(strings.TrimSpace(order.FillPrice), 64)
	if filledQty <= 0 {
		if persisted, ok := persistedGateLayerFill(layer); ok {
			return persisted, true, nil
		}
		return gateLayerFill{}, false, nil
	}
	if priceErr != nil || fillPrice <= 0 {
		if persisted, ok := persistedGateLayerFill(layer); ok {
			fillPrice = persisted.price
		} else {
			trades, tradeErr := client.MyTrades(ctx, contract)
			if tradeErr != nil {
				return gateLayerFill{}, false, tradeErr
			}
			var value float64
			var tradeQty float64
			for _, trade := range trades {
				if strings.TrimSpace(trade.OrderID) != orderID {
					continue
				}
				qty, qtyErr := strconv.ParseFloat(strings.TrimSpace(trade.Size), 64)
				price, tradePriceErr := strconv.ParseFloat(strings.TrimSpace(trade.Price), 64)
				if qtyErr != nil || tradePriceErr != nil || price <= 0 {
					continue
				}
				tradeQty += math.Abs(qty)
				value += math.Abs(qty) * price
			}
			if tradeQty > 0 {
				filledQty = tradeQty
				fillPrice = value / tradeQty
			}
		}
	}
	if fillPrice <= 0 || math.IsNaN(fillPrice) || math.IsInf(fillPrice, 0) {
		return gateLayerFill{}, false, fmt.Errorf("Gate order %s has no positive fill price", orderID)
	}
	return gateLayerFill{quantity: filledQty, price: fillPrice, orderSize: total}, true, nil
}

func persistedGateLayerFill(layer tvbottrading.OrderLayerResult) (gateLayerFill, bool) {
	quantity, qtyErr := strconv.ParseFloat(strings.TrimSpace(layer.FilledQty), 64)
	price, priceErr := strconv.ParseFloat(strings.TrimSpace(layer.FillPrice), 64)
	if qtyErr != nil || priceErr != nil || quantity <= 0 || price <= 0 {
		return gateLayerFill{}, false
	}
	return gateLayerFill{quantity: quantity, price: price}, true
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

func gateLayerRiskPlans(record tvbotstorage.OrderRecord, layer tvbottrading.OrderLayerResult, index int, fill gateLayerFill, priceStep string, enableDecimal bool, apiID, marginMode string) ([]gateRiskPlan, error) {
	if fill.quantity <= 0 || fill.price <= 0 {
		return nil, errors.New("Gate layer fill must have positive quantity and price")
	}
	leverage := record.Result.Leverage
	if leverage <= 0 {
		leverage = record.Leverage
	}
	if leverage <= 0 {
		return nil, errors.New("Gate layer leverage is unavailable")
	}
	action := record.Action
	if fill.orderSize < 0 {
		action = tvbottrading.ActionShort
	} else if fill.orderSize > 0 {
		action = tvbottrading.ActionLong
	}
	tp, sl, err := tvbottrading.FixedTPSLTriggerPrices(action, fill.price, record.Risk.TPPct.Value, record.Risk.SLPct.Value, leverage)
	if err != nil {
		return nil, err
	}
	contract := strings.ToUpper(strings.TrimSpace(record.Result.InstID))
	if contract == "" {
		return nil, errors.New("Gate layer contract is unavailable")
	}
	long := action == tvbottrading.ActionLong
	orderType := "plan-close-long-position"
	closeSide := "SELL"
	closeSize := "-" + tvbottrading.NormalizeFloat(fill.quantity)
	tpRule, slRule := int64(1), int64(2)
	if !long {
		orderType = "plan-close-short-position"
		closeSide = "BUY"
		closeSize = tvbottrading.NormalizeFloat(fill.quantity)
		tpRule, slRule = 2, 1
	}
	closeAmount := ""
	if enableDecimal {
		closeAmount = closeSize
		closeSize = "0"
	}
	layerKey := strings.TrimSpace(layer.OrdID)
	if layerKey == "" {
		layerKey = strings.TrimSpace(layer.ClOrdID)
	}
	if layerKey == "" {
		layerKey = fmt.Sprintf("layer-%d", index)
	}
	return []gateRiskPlan{
		{contract: contract, kind: "tp", orderType: orderType, triggerPx: tvbotgate.FormatPriceStep(tp, priceStep), rule: tpRule, clientText: gateRiskClientText(apiID, contract, record.SignalID, layerKey, "tp"), resultType: "TAKE_PROFIT", closeSide: closeSide, marginMode: marginMode, size: closeSize, amount: closeAmount, quantity: tvbottrading.NormalizeFloat(fill.quantity)},
		{contract: contract, kind: "sl", orderType: orderType, triggerPx: tvbotgate.FormatPriceStep(sl, priceStep), rule: slRule, clientText: gateRiskClientText(apiID, contract, record.SignalID, layerKey, "sl"), resultType: "STOP_LOSS", closeSide: closeSide, marginMode: marginMode, size: closeSize, amount: closeAmount, quantity: tvbottrading.NormalizeFloat(fill.quantity)},
	}, nil
}

func managedGateRiskOrdersForText(orders []tvbotgate.PriceTriggeredOrder, text string) []tvbotgate.PriceTriggeredOrder {
	managed := make([]tvbotgate.PriceTriggeredOrder, 0, 1)
	for _, order := range orders {
		if order.Initial.Text == text {
			managed = append(managed, order)
		}
	}
	return managed
}

func gateRiskOrderResult(desired gateRiskPlan, orderID string) tvbottrading.RiskOrderResult {
	return tvbottrading.RiskOrderResult{
		Exchange:     tvbottrading.ExchangeGate,
		AlgoID:       orderID,
		ClientAlgoID: desired.clientText,
		OrderType:    desired.resultType,
		Side:         desired.closeSide,
		Quantity:     desired.quantity,
		TriggerPrice: desired.triggerPx,
	}
}

func (s *Server) ensureGateRiskOrder(ctx context.Context, client tvbotgate.Client, desired gateRiskPlan, existing []tvbotgate.PriceTriggeredOrder) (string, error) {
	if len(existing) > 0 {
		keep := existing[0]
		for _, duplicate := range existing[1:] {
			if err := cancelGatePriceTriggeredOrder(ctx, client, duplicate); err != nil && s.Logger != nil {
				s.Logger.Warn("failed to cancel duplicate Gate layered TP/SL order", "order_id", duplicate.IDString(), "error", err)
			}
		}
		if keep.OrderType != desired.orderType || !gatePriceOrderIsReduceOnly(keep) || gatePriceOrderIsClose(keep) {
			if err := cancelGatePriceTriggeredOrder(ctx, client, keep); err != nil {
				return "", fmt.Errorf("replace Gate %s order %s: %w", desired.kind, keep.IDString(), err)
			}
			return s.ensureGateRiskOrder(ctx, client, desired, nil)
		}
		if keep.Trigger.Price == desired.triggerPx && keep.Trigger.Rule == desired.rule && gatePriceOrderHasQuantity(keep, desired) {
			return keep.IDString(), nil
		}
		response, err := client.AmendPriceTriggeredOrder(ctx, keep.IDString(), tvbotgate.PriceTriggeredOrderAmendment{
			OrderID:   keep.IDString(),
			Size:      desired.size,
			Amount:    desired.amount,
			Price:     "0",
			TriggerPx: desired.triggerPx,
			PriceType: 0,
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
			Size:       desired.size,
			Amount:     desired.amount,
			Price:      "0",
			TIF:        "ioc",
			Text:       desired.clientText,
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

func gatePriceOrderHasQuantity(order tvbotgate.PriceTriggeredOrder, desired gateRiskPlan) bool {
	actual := strings.TrimSpace(order.Initial.Size)
	want := strings.TrimSpace(desired.size)
	if strings.TrimSpace(desired.amount) != "" {
		actual = strings.TrimSpace(order.Initial.Amount)
		want = strings.TrimSpace(desired.amount)
	}
	got, gotErr := strconv.ParseFloat(actual, 64)
	wantValue, wantErr := strconv.ParseFloat(want, 64)
	return gotErr == nil && wantErr == nil && math.Abs(got-wantValue) < 1e-9
}

func clearGateRiskResult(result *tvbottrading.OrderResult) bool {
	if result == nil {
		return false
	}
	changed := len(result.RiskOrders) > 0
	result.RiskOrders = nil
	for i := range result.Layers {
		if len(result.Layers[i].RiskOrders) > 0 {
			changed = true
			result.Layers[i].RiskOrders = nil
		}
	}
	return changed
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

func gateRiskClientText(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "|")))
	kind := "risk"
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) != "" {
		kind = strings.TrimSpace(parts[len(parts)-1])
	}
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
