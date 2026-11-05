package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestAutomaticPositionCloseRequiresAtLeast15Minutes(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	monitor := tvbotconfig.PositionMonitorConfig{MinHoldingMinutes: 15, TakeProfitPct: 5}
	position := tvbotokx.Position{InstID: "BTC-USDT-SWAP", PosSide: "long", Pos: "1", UplRatio: "0.10"}
	entry := positionEntryFill{InstID: position.InstID, PosSide: "long", Side: "buy", Size: 1, FillTime: now.Add(-(15*time.Minute - time.Second))}
	if trigger, hit, _, _, _ := positionMonitorTriggerForPosition(position, monitor, true, now, []positionEntryFill{entry}, nil, false); hit || trigger != "" {
		t.Fatal("14:59 automatic close must be skipped")
	}
	entry.FillTime = now.Add(-15 * time.Minute)
	if trigger, hit, _, held, err := positionMonitorTriggerForPosition(position, monitor, true, now, []positionEntryFill{entry}, nil, false); !hit || trigger != "take_profit" || held != 15*time.Minute || err != "" {
		t.Fatalf("15:00 automatic close should be allowed: trigger=%q hit=%v held=%s err=%q", trigger, hit, held, err)
	}
}

func TestAutomaticPositionCloseSkipsWhenEntryTimeIsUnavailable(t *testing.T) {
	now := time.Now().UTC()
	monitor := tvbotconfig.PositionMonitorConfig{MinHoldingMinutes: 15, TakeProfitPct: 5}
	position := tvbotokx.Position{InstID: "BTC-USDT-SWAP", PosSide: "long", Pos: "1", UplRatio: "0.10"}
	if trigger, hit, _, _, err := positionMonitorTriggerForPosition(position, monitor, true, now, nil, nil, false); hit || trigger != "" || !strings.Contains(err, "无法") {
		t.Fatalf("missing entry time must fail closed: trigger=%q hit=%v err=%q", trigger, hit, err)
	}
}

func TestProtectionOrdersDisabledRejectsCreation(t *testing.T) {
	srv := newTestServer(t)
	cfg := srv.ConfigStore.Get()
	cfg.Trading.ProtectionOrdersEnabled = false
	srv.ConfigStore = tvbotconfig.NewStore("", cfg)
	req := httptest.NewRequest(http.MethodPost, "/tvbot/positions/protection", strings.NewReader(`{"exchange":"okx","env":"demo","inst_id":"BTC-USDT-SWAP","pos_side":"long","kind":"sl"}`))
	rec := httptest.NewRecorder()
	srv.handlePositionProtection(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "protection_disabled") {
		t.Fatalf("disabled protection request was not rejected: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManualLiquidationRequiresAutomaticHoldingGate(t *testing.T) {
	now := time.Now().UTC()
	cfg := tvbotconfig.Default()
	position := tvbotokx.Position{InstID: "BTC-USDT-SWAP", PosSide: "long", Pos: "1"}
	signal := tvbottrading.Signal{PositionEffect: tvbottrading.PositionEffectClose, Condition: "manual_liquidation_trigger"}
	entry := positionEntryFill{InstID: position.InstID, PosSide: "long", Side: "buy", Size: 1, FillTime: now.Add(-time.Minute)}
	if err := enforceAutomaticCloseMinimumHolding(signal, cfg, position, []positionEntryFill{entry}, nil, false, now); !errors.Is(err, errMinAutomaticHoldingNotReached) {
		t.Fatalf("manual liquidation before 15 minutes should be blocked: %v", err)
	}
}

func TestProtectionCleanupPreservesOrdinaryOrders(t *testing.T) {
	if isBinanceLegacyProtectionOrder(tvbotbinance.OpenOrder{ReduceOnly: true, Type: "LIMIT"}) {
		t.Fatal("ordinary Binance reduce-only limit close must be preserved")
	}
	if !isBinanceLegacyProtectionOrder(tvbotbinance.OpenOrder{ReduceOnly: true, Type: "STOP_MARKET"}) {
		t.Fatal("Binance reduce-only stop protection must be cleaned")
	}
	if isBinanceProtectionOrder(tvbotbinance.AlgoOpenOrder{ReduceOnly: false, OrderType: "STOP_MARKET"}) {
		t.Fatal("ordinary Binance conditional entry must be preserved")
	}
	if gateProtectionOrder(tvbotgate.PriceTriggeredOrder{Initial: tvbotgate.PriceTriggeredInitial{Contract: "BTC_USDT", Size: "1", Price: "64000"}}) {
		t.Fatal("ordinary Gate trigger order must be preserved")
	}
	if !gateProtectionOrder(tvbotgate.PriceTriggeredOrder{Initial: tvbotgate.PriceTriggeredInitial{Contract: "BTC_USDT", Size: "0", Price: "64000", Close: true}}) {
		t.Fatal("Gate close trigger protection must be cleaned")
	}
}
