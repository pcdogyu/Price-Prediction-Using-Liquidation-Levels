package app

import (
	"strings"
	"testing"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestSignalMessageAggressiveOmitsSteadyConfirmationPrompt(t *testing.T) {
	message := signalMessage(domain.TradeSignal{
		Symbol:              "ZECUSDT",
		Kind:                domain.SignalAggressive,
		Side:                "short",
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
