package app

import (
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestAlertMessageOmitsQuietPeriodDetails(t *testing.T) {
	message := alertMessage(domain.LiquidationAlert{
		Symbol:        "BTCUSDT",
		Side:          "long",
		TradeSide:     "short",
		WindowMinutes: 3,
		NotionalUSD:   3470000,
		PeakUSD:       3470000,
		ThresholdUSD:  300000,
		EventCount:    21,
		QuietSeconds:  600,
		QuietUntil:    time.Date(2026, 10, 16, 3, 53, 25, 0, time.UTC),
	})

	for _, unwanted := range []string{"安静观察期", "安静截止"} {
		if strings.Contains(message, unwanted) {
			t.Fatalf("alert contains removed text %q: %q", unwanted, message)
		}
	}

	for _, expected := range []string{"BTCUSDT", "多单被爆仓 → 观察下单方向：做空", "3 分钟爆仓额：$3.47M（阈值 $300K）", "笔数 21 · 峰值 $3.47M"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("alert is missing %q: %q", expected, message)
		}
	}
}

func TestSignalMessageAggressiveOmitsSteadyConfirmationPrompt(t *testing.T) {
	message := signalMessage(domain.TradeSignal{
		Symbol:              "ZECUSDT",
		Kind:                domain.SignalAggressive,
		Side:                "long",
		LiquidationSide:     "short",
		WindowMinutes:       3,
		AlertNotionalUSD:    398000,
		ThresholdUSD:        300000,
		QuietElapsedSeconds: 600,
		FollowUps:           5,
		ReferencePrice:      1229.96,
		ConfirmLimitSecs:    1800,
	})

	for _, unwanted := range []string{"等待反向爆仓确认", "→ 稳健信号"} {
		if strings.Contains(message, unwanted) {
			t.Fatalf("aggressive signal contains removed text %q: %q", unwanted, message)
		}
	}

	for _, expected := range []string{"ZECUSDT", "预警：3 分钟空单爆仓 $398K（阈值 $300K）", "同向爆仓安静 10 分 00 秒（顺延 5 次）", "参考价：$1229.96"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("aggressive signal is missing %q: %q", expected, message)
		}
	}
}
