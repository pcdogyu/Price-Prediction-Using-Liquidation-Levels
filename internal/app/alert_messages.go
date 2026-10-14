package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// The Telegram texts are written in Chinese to match the alert page, and every
// message states the direction as an order side, because that is what the user
// trades: a long liquidation wave is traded long.

func alertMessage(alert domain.LiquidationAlert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🚨 爆仓预警 · %s\n", alert.Symbol)
	fmt.Fprintf(&b, "%s → 观察下单方向：%s\n", liquidationLabel(alert.Side), orderLabel(alert.TradeSide))
	fmt.Fprintf(&b, "%d 分钟爆仓额：%s（阈值 %s）\n", alert.WindowMinutes, formatUSD(alert.NotionalUSD), formatUSD(alert.ThresholdUSD))
	fmt.Fprintf(&b, "笔数 %d · 峰值 %s\n", alert.EventCount, formatUSD(alert.PeakUSD))
	fmt.Fprintf(&b, "安静观察期 %s，期间每笔同向爆仓都会顺延\n", formatDuration(time.Duration(alert.QuietSeconds)*time.Second))
	fmt.Fprintf(&b, "安静截止：%s\n", alert.QuietUntil.UTC().Format("2006-01-02 15:04:05 UTC"))
	return b.String()
}

func signalMessage(signal domain.TradeSignal) string {
	var b strings.Builder
	switch signal.Kind {
	case domain.SignalAggressive:
		fmt.Fprintf(&b, "⚡ 激进信号 · %s %s\n", signal.Symbol, orderLabel(signal.Side))
		fmt.Fprintf(&b, "预警：%d 分钟%s爆仓 %s（阈值 %s）\n", signal.WindowMinutes, liquidationPrefix(signal.LiquidationSide), formatUSD(signal.AlertNotionalUSD), formatUSD(signal.ThresholdUSD))
		fmt.Fprintf(&b, "同向爆仓安静 %s（顺延 %d 次）\n", formatDuration(time.Duration(signal.QuietElapsedSeconds)*time.Second), signal.FollowUps)
		if signal.ReferencePrice > 0 {
			fmt.Fprintf(&b, "参考价：%s\n", formatPrice(signal.ReferencePrice))
		}
		fmt.Fprintf(&b, "等待反向爆仓确认（%s 内）→ 稳健信号", formatDuration(time.Duration(signal.ConfirmLimitSecs)*time.Second))
	case domain.SignalSteady:
		fmt.Fprintf(&b, "✅ 稳健信号 · %s %s（已确认）\n", signal.Symbol, orderLabel(signal.Side))
		fmt.Fprintf(&b, "激进信号后 %s 出现反向爆仓 %s，共 %d 笔\n", formatDuration(time.Duration(signal.OppositeSeconds)*time.Second), formatUSD(signal.OppositeUSD), signal.OppositeCount)
		fmt.Fprintf(&b, "原预警：%d 分钟%s爆仓 %s\n", signal.WindowMinutes, liquidationPrefix(signal.LiquidationSide), formatUSD(signal.AlertNotionalUSD))
		if signal.ReferencePrice > 0 {
			fmt.Fprintf(&b, "确认时价格：%s", formatPrice(signal.ReferencePrice))
		}
	default:
		return "爆仓信号：" + signal.Symbol + " " + signal.Side
	}
	return b.String()
}

func testMessage(chatID string, thresholds domain.AlertThresholds) string {
	var b strings.Builder
	b.WriteString("🔔 爆仓报警通知测试成功\n")
	fmt.Fprintf(&b, "Chat ID：%s\n", chatID)
	fmt.Fprintf(&b, "规则：%d 分钟内同向爆仓额超过阈值 → 预警；安静 %d 分钟无反弹 → 激进信号；%d 分钟内反向爆仓 → 稳健信号\n", thresholds.WindowMinutes, thresholds.QuietMinutes, thresholds.ConfirmMinutes)
	fmt.Fprintf(&b, "阈值：BTC %s · ETH %s · 其他 %s · 监控前 %d 个成交额币对", formatUSD(thresholds.BTCUSD), formatUSD(thresholds.ETHUSD), formatUSD(thresholds.OtherUSD), thresholds.TopSymbols)
	return b.String()
}

func liquidationLabel(side string) string {
	if side == "long" {
		return "多单被爆仓"
	}
	return "空单被爆仓"
}

func liquidationPrefix(side string) string {
	if side == "long" {
		return "多单"
	}
	return "空单"
}

func orderLabel(side string) string {
	if side == "long" {
		return "做多"
	}
	return "做空"
}

func formatUSD(value float64) string {
	switch {
	case value >= 1e9:
		return fmt.Sprintf("$%.2fB", value/1e9)
	case value >= 1e6:
		return fmt.Sprintf("$%.2fM", value/1e6)
	case value >= 1e3:
		return fmt.Sprintf("$%.0fK", value/1e3)
	default:
		return fmt.Sprintf("$%.0f", value)
	}
}

func formatPrice(value float64) string {
	switch {
	case value >= 1000:
		return fmt.Sprintf("$%.2f", value)
	case value >= 1:
		return fmt.Sprintf("$%.4f", value)
	default:
		return fmt.Sprintf("$%.6f", value)
	}
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	minutes := int64(d.Minutes())
	seconds := int64(d.Seconds()) - minutes*60
	switch {
	case minutes >= 60:
		return fmt.Sprintf("%d 小时 %d 分", minutes/60, minutes%60)
	case minutes > 0:
		return fmt.Sprintf("%d 分 %02d 秒", minutes, seconds)
	default:
		return fmt.Sprintf("%d 秒", seconds)
	}
}
