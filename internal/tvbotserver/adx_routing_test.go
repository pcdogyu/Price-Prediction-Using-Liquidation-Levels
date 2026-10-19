package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestTVOrderADX410RoutingThresholdsAndDirections(t *testing.T) {
	tests := []struct {
		name         string
		action       trading.Side
		intent       string
		adx          string
		wantAction   trading.Side
		wantSide     string
		wantEffect   string
		wantStrategy string
		wantIgnore   bool
	}{
		{name: "trend long", action: trading.ActionLong, intent: "entry_long", adx: "25.01", wantAction: trading.ActionLong, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyTrend},
		{name: "scalp long becomes short", action: trading.ActionLong, intent: "entry_long", adx: "19.99", wantAction: trading.ActionShort, wantSide: trading.PositionSideShort, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyScalp},
		{name: "scalp short becomes long", action: trading.ActionShort, intent: "entry_short", adx: "0", wantAction: trading.ActionLong, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyScalp},
		{name: "boundary 20 ignored", action: trading.ActionLong, intent: "entry_long", adx: "20", wantAction: trading.ActionLong, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyTransition, wantIgnore: true},
		{name: "middle ignored", action: trading.ActionShort, intent: "entry_short", adx: "22.5", wantAction: trading.ActionShort, wantSide: trading.PositionSideShort, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyTransition, wantIgnore: true},
		{name: "boundary 25 ignored", action: trading.ActionLong, intent: "entry_long", adx: "25", wantAction: trading.ActionLong, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyTransition, wantIgnore: true},
		{name: "scalp closes original long as actual short", action: trading.ActionShort, intent: "tp_long", adx: "18.5", wantAction: trading.ActionLong, wantSide: trading.PositionSideShort, wantEffect: trading.PositionEffectClose, wantStrategy: trading.MarketStrategyScalp},
		{name: "scalp closes original short as actual long", action: trading.ActionLong, intent: "sl_short", adx: "18.5", wantAction: trading.ActionShort, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectClose, wantStrategy: trading.MarketStrategyScalp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.intent + "|script=4.1.0|adx=" + tc.adx + "|adx_tf=60"
			signal := trading.Signal{Action: tc.action, OrderIntent: message, Text: message}
			signal.Normalize()
			signal.SourceAction = signal.Action
			if err := applyTVOrderPositionSemantics(&signal); err != nil {
				t.Fatal(err)
			}
			decision, err := applyTVOrderADXRouting(&signal)
			if err != nil {
				t.Fatal(err)
			}
			if !decision.Versioned || decision.IgnoreEntry != tc.wantIgnore || signal.Action != tc.wantAction || signal.SourceAction != tc.action || signal.PositionSide != tc.wantSide || signal.PositionEffect != tc.wantEffect || signal.MarketStrategy != tc.wantStrategy {
				t.Fatalf("bad routing decision=%#v signal=%#v", decision, signal)
			}
			if signal.ADX == nil {
				t.Fatalf("ADX was not stored: %#v", signal)
			}
		})
	}
}

func TestTVOrderADX411RoutingThresholdsAndDirections(t *testing.T) {
	tests := []struct {
		name         string
		action       trading.Side
		intent       string
		adx          string
		wantAction   trading.Side
		wantSide     string
		wantEffect   string
		wantStrategy string
	}{
		{name: "boundary 25 is trend", action: trading.ActionLong, intent: "entry_long", adx: "25", wantAction: trading.ActionLong, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyTrend},
		{name: "above 25 is trend", action: trading.ActionShort, intent: "entry_short", adx: "25.01", wantAction: trading.ActionShort, wantSide: trading.PositionSideShort, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyTrend},
		{name: "below 25 long becomes short", action: trading.ActionLong, intent: "entry_long", adx: "24.99", wantAction: trading.ActionShort, wantSide: trading.PositionSideShort, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyScalp},
		{name: "below 25 short becomes long", action: trading.ActionShort, intent: "entry_short", adx: "0", wantAction: trading.ActionLong, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectOpen, wantStrategy: trading.MarketStrategyScalp},
		{name: "below 25 closes original long as actual short", action: trading.ActionShort, intent: "tp_long", adx: "24.99", wantAction: trading.ActionLong, wantSide: trading.PositionSideShort, wantEffect: trading.PositionEffectClose, wantStrategy: trading.MarketStrategyScalp},
		{name: "below 25 closes original short as actual long", action: trading.ActionLong, intent: "sl_short", adx: "24.99", wantAction: trading.ActionShort, wantSide: trading.PositionSideLong, wantEffect: trading.PositionEffectClose, wantStrategy: trading.MarketStrategyScalp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.intent + "|script=4.1.1|adx=" + tc.adx + "|adx_tf=60"
			signal := trading.Signal{Action: tc.action, OrderIntent: message, Text: message}
			signal.Normalize()
			signal.SourceAction = signal.Action
			if err := applyTVOrderPositionSemantics(&signal); err != nil {
				t.Fatal(err)
			}
			decision, err := applyTVOrderADXRouting(&signal)
			if err != nil {
				t.Fatal(err)
			}
			if !decision.Versioned || decision.IgnoreEntry || signal.Action != tc.wantAction || signal.SourceAction != tc.action || signal.PositionSide != tc.wantSide || signal.PositionEffect != tc.wantEffect || signal.MarketStrategy != tc.wantStrategy {
				t.Fatalf("bad routing decision=%#v signal=%#v", decision, signal)
			}
			if signal.ADX == nil {
				t.Fatalf("ADX was not stored: %#v", signal)
			}
		})
	}
}

func TestTVOrderADXMetadataValidationAndLegacyCompatibility(t *testing.T) {
	invalid := []string{
		"entry_long|script=4.1.0|adx_tf=60",
		"entry_long|script=4.1.0|adx=abc|adx_tf=60",
		"entry_long|script=4.1.0|adx=101|adx_tf=60",
		"entry_long|script=4.1.0|adx=18|adx=19|adx_tf=60",
		"entry_long|script=4.1.0|script=4.1.0|adx=18|adx_tf=60",
		"entry_long|script=4.1.0|adx=18",
		"entry_long|script=4.1.1|adx_tf=60",
		"entry_long|script=4.1.1|adx=abc|adx_tf=60",
		"entry_long|script=4.1.1|adx=101|adx_tf=60",
		"entry_long|script=4.1.1|adx=24|adx=23|adx_tf=60",
		"entry_long|script=4.1.1|script=4.1.1|adx=24|adx_tf=60",
		"entry_long|script=4.1.1|adx=24",
	}
	for _, message := range invalid {
		signal := trading.Signal{Action: trading.ActionLong, OrderIntent: message}
		signal.Normalize()
		signal.SourceAction = signal.Action
		if err := applyTVOrderPositionSemantics(&signal); err != nil {
			t.Fatal(err)
		}
		if _, err := applyTVOrderADXRouting(&signal); err == nil {
			t.Fatalf("expected invalid metadata error for %q", message)
		}
	}

	legacy := trading.Signal{Action: trading.ActionLong, OrderIntent: "entry_long"}
	legacy.Normalize()
	legacy.SourceAction = legacy.Action
	if err := applyTVOrderPositionSemantics(&legacy); err != nil {
		t.Fatal(err)
	}
	decision, err := applyTVOrderADXRouting(&legacy)
	if err != nil || decision.Versioned || legacy.Action != trading.ActionLong || legacy.MarketStrategy != "" || legacy.ADX != nil {
		t.Fatalf("legacy alert should remain unchanged decision=%#v signal=%#v err=%v", decision, legacy, err)
	}

	unknown := trading.Signal{Action: trading.ActionShort, OrderIntent: "entry_short|script=4.1.2|adx=24|adx_tf=60"}
	unknown.Normalize()
	unknown.SourceAction = unknown.Action
	if err := applyTVOrderPositionSemantics(&unknown); err != nil {
		t.Fatal(err)
	}
	decision, err = applyTVOrderADXRouting(&unknown)
	if err != nil || decision.Versioned || unknown.Action != trading.ActionShort || unknown.MarketStrategy != "" || unknown.ADX != nil {
		t.Fatalf("unknown alert version should remain unchanged decision=%#v signal=%#v err=%v", decision, unknown, err)
	}
}

func TestTVOrderADX411ScalpRoutesBeforeExecutionAndPersistence(t *testing.T) {
	for _, exchange := range []string{trading.ExchangeOKX, trading.ExchangeBinance} {
		t.Run(exchange, func(t *testing.T) {
			srv := newTestServer(t)
			signal := validSignal(t, srv)
			signal.TargetExchange = exchange
			signal.OrderIntent = "entry_long|script=4.1.1|adx=24.42|adx_tf=60"
			signal.Text = signal.OrderIntent
			body, err := json.Marshal(signal)
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			response := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
			select {
			case executed := <-srv.Executor.(fakeExecutor).calls:
				if executed.SourceAction != trading.ActionLong || executed.Action != trading.ActionShort || executed.PositionSide != trading.PositionSideShort || executed.MarketStrategy != trading.MarketStrategyScalp || executed.ADX == nil || *executed.ADX != 24.42 || executed.TargetExchange != exchange {
					t.Fatalf("bad executed signal: %#v", executed)
				}
			case <-time.After(time.Second):
				t.Fatal("executor was not called")
			}
			record := waitOrderStatus(t, srv.Orders, response.SignalID, storage.StatusSubmitted)
			if record.SourceAction != trading.ActionLong || record.Action != trading.ActionShort || record.MarketStrategy != trading.MarketStrategyScalp || record.ADX == nil || *record.ADX != 24.42 {
				t.Fatalf("bad persisted signal: %#v", record)
			}
		})
	}
}

func TestTVOrderADXTransitionIsIgnoredWithoutExecution(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.OrderIntent = "entry_long|script=4.1.0|adx=20|adx_tf=60"
	signal.Text = signal.OrderIntent
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"reason":"adx_transition"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case executed := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("transition alert should not execute: %#v", executed)
	case <-time.After(50 * time.Millisecond):
	}
	records := srv.Orders.List(10)
	if len(records) != 1 || records[0].Status != storage.StatusIgnored || records[0].ErrorCode != "adx_transition" || records[0].MarketStrategy != trading.MarketStrategyTransition {
		t.Fatalf("bad ignored record: %#v", records)
	}
}

func TestTVOrderInvalidVersionedADXIsRejected(t *testing.T) {
	srv := newTestServer(t)
	signal := validSignal(t, srv)
	signal.OrderIntent = "entry_long|script=4.1.1|adx_tf=60"
	signal.Text = signal.OrderIntent
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_adx_metadata") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case executed := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("invalid alert should not execute: %#v", executed)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPine410ContainsADXAlertContract(t *testing.T) {
	body, err := os.ReadFile("../../tradingview/SMC_Order_Block_Only_Test_Strategy_v4_1_0.pine")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	markers := []string{
		`const string SCRIPT_VERSION = "4.1.0"`,
		`adxTimeframe = input.timeframe(`,
		`f_adx(adxDiLength, adxSmoothing)[1]`,
		`bool adxRoutingReady = adxTrendMarket or adxScalpMarket`,
		`"|script=" + SCRIPT_VERSION`,
		`"|adx=" + str.tostring(_adx, "#.####")`,
		`activeEntryAdx := submittedEntryAdx`,
		`alert_profit = tpLongAlertMessage`,
		`alert_loss = slShortAlertMessage`,
	}
	for _, marker := range markers {
		if !strings.Contains(text, marker) {
			t.Fatalf("Pine 4.1.0 missing %q", marker)
		}
	}
}

func TestPine411ContainsADXAlertContract(t *testing.T) {
	body, err := os.ReadFile("../../tradingview/SMC_Order_Block_Only_Test_Strategy_v4_1_1.pine")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	markers := []string{
		`const string SCRIPT_VERSION = "4.1.1"`,
		`adxTimeframe = input.timeframe(`,
		`f_adx(adxDiLength, adxSmoothing)[1]`,
		`bool adxTrendMarket = not na(confirmedAdx) and confirmedAdx >= 25.0`,
		`bool adxScalpMarket = not na(confirmedAdx) and confirmedAdx < 25.0`,
		`bool adxRoutingReady = not na(confirmedAdx)`,
		`adxScalpMarket ? "震荡行情"`,
		`"|script=" + SCRIPT_VERSION`,
		`"|adx=" + str.tostring(_adx, "#.####")`,
		`activeEntryAdx := submittedEntryAdx`,
		`alert_profit = tpLongAlertMessage`,
		`alert_loss = slShortAlertMessage`,
	}
	for _, marker := range markers {
		if !strings.Contains(text, marker) {
			t.Fatalf("Pine 4.1.1 missing %q", marker)
		}
	}
}
