package server

import (
	"strings"
	"testing"
)

func TestTVBotUSDTBalanceLayoutAndWindowButtons(t *testing.T) {
	if got := strings.Count(tvbotHTML, `class="analysis-metrics symbol-metrics exchange-balance-metrics"`); got != 2 {
		t.Fatalf("exchange balance metric blocks=%d, want 2", got)
	}
	if got := strings.Count(tvbotHTML, `class="analysis-exchange-block balance-pnl-block"`); got != 2 {
		t.Fatalf("embedded pnl blocks=%d, want 2", got)
	}
	for _, marker := range []string{
		`.exchange-balance-metrics`,
		`.balance-pnl-block`,
		`.analysis-period-row`,
		`.analysis-time-status`,
		`.analysis-usdt-chart-card`,
		`.global-exchange-switch`,
		`data-global-exchange="okx"`,
		`data-global-exchange="binance"`,
		`data-exchange-view="okx"`,
		`data-exchange-view="binance"`,
		`globalExchangeStorageKey = "tvbot.selectedExchange"`,
		`window.localStorage.getItem(globalExchangeStorageKey)`,
		`window.localStorage.setItem(globalExchangeStorageKey, selected)`,
		`function activeExchange()`,
		`function setSelectedExchange(exchange)`,
		`function renderGlobalExchangeSwitch()`,
		`订单时间`,
		`OKX 订单`,
		`Binance 订单`,
		`USDT 估值表`,
		`USDT 权益图`,
		`id="analysis-okx-window-start"`,
		`id="analysis-okx-window-change"`,
		`id="analysis-okx-max-drawdown"`,
		`id="analysis-binance-window-start"`,
		`id="analysis-binance-window-change"`,
		`id="analysis-binance-max-drawdown"`,
		`id="analysis-okx-net-pnl"`,
		`id="analysis-binance-net-pnl"`,
		`id="analysis-okx-turnover"`,
		`id="analysis-okx-fees"`,
		`id="analysis-okx-position-count"`,
		`id="analysis-okx-position-upl"`,
		`id="analysis-binance-turnover"`,
		`id="analysis-binance-fees"`,
		`id="analysis-binance-position-count"`,
		`id="analysis-binance-position-upl"`,
		`#analysis .mini-usdt-chart`,
		`height: 360px`,
		`height: 346px`,
		`miniMinHeight = isAnalysisChart ? 360 : 240`,
		`miniFallbackHeight = isAnalysisChart ? 360 : 250`,
		`function formatUSDTBalance(v)`,
		`Math.round(n).toLocaleString`,
		`const formatted = formatUSDTBalance(v)`,
		`function usdtBalanceRawValue(row)`,
		`const raw = row && row.eq`,
		`value: Number(usdtBalanceRawValue(point))`,
		`function usdtBalancePoints(balancePoints, balance)`,
		`"USDT 权益图 " + balanceWindowLabel(state.balanceWindowMinutes)`,
		`暂无 OKX USDT 权益数据`,
		`暂无 Binance USDT 权益数据`,
		`.balance-window-toolbar .balance-window-btn`,
		`font-size: 16px`,
		`data-balance-minutes="15"`,
		`data-balance-minutes="60"`,
		`data-balance-minutes="240"`,
		`data-balance-minutes="480"`,
		`data-balance-minutes="720"`,
		`data-balance-minutes="1440"`,
		`data-balance-minutes="2880"`,
		`data-balance-minutes="4320"`,
		`data-balance-minutes="10080"`,
		`下单盈亏分析`,
		`class="symbol-table analysis-trade-table"`,
		`min-width: 1560px`,
		`id="analysis-trade-cols"`,
		`id="analysis-trade-head"`,
		`id="analysis-trade-rows"`,
		`id="analysis-trade-page-info"`,
		`id="analysis-trade-prev"`,
		`id="analysis-trade-next"`,
		`analysisTradePageSize = 20`,
		`analysisTradeColumnStorageKey = "tvbot.analysisTradeColumns.v3"`,
		`window.localStorage.getItem(analysisTradeColumnStorageKey)`,
		`window.localStorage.setItem(analysisTradeColumnStorageKey`,
		`normalizeAnalysisTradeColumnOrder`,
		`data-analysis-trade-column`,
		`cols.innerHTML = columns.map`,
		`handleAnalysisTradeColumnDrop`,
		`analysis-exit-time-col`,
		`analysis-inst-id-col`,
		`analysis-side-col`,
		`title: "保证金"`,
		`title: "杠杆"`,
		`title: "开仓价"`,
		`title: "平仓价"`,
		`title: "盈亏比"`,
		`title: "成交额"`,
		`title: "净盈亏"`,
		`function analysisAmountText(value)`,
		`ccy.toUpperCase() !== "USDT"`,
		`function analysisInstIDText(instID)`,
		`function analysisPnLRatioText(row)`,
		`function analysisPositionSideToneClass(side)`,
		`function renderAnalysisTradeHistory(errorText)`,
		`analysisTradesForActiveExchange`,
		`position_trades`,
		`function loadAnalysisPositions(forceRefresh)`,
		`暂无 ' + escapeHTML(label) + ' 下单盈亏记录`,
		`exchange: activeExchange()`,
		`loadPositionExchange(activeExchange()`,
		`loadPendingOrdersExchange(activeExchange()`,
		`缓存数据（上游暂不可用）`,
		`item.status === "stale"`,
		`balance && usdtBalanceDetail(balance)`,
	} {
		if !strings.Contains(tvbotHTML, marker) {
			t.Fatalf("tvbot ui missing %s", marker)
		}
	}
	for _, old := range []string{
		`OKX USDT 余额表`,
		`Binance USDT 余额表`,
		`OKX USDT估值`,
		`Binance USDT估值`,
		`usdtValuationPoints`,
		`估值 USD`,
		`analysis-total-eq`,
		`analysis-avail-eq`,
		`analysis-adj-eq`,
		`analysis-asset-count`,
		`analysis-binance-avail`,
		`id="analysis-binance-api"`,
		`id="analysis-okx-api-id"`,
		`id="analysis-binance-api-id"`,
		`id="refresh-analysis"`,
		`OKX API<select id="analysis-okx-api-id"></select>`,
		`Binance API<select id="analysis-binance-api-id"></select>`,
		`刷新分析`,
		`<tr><th>时间</th><th>币对</th><th>方向</th><th>成交价</th><th>数量</th><th>盈亏</th><th>手续费</th><th>订单号</th><th>成交笔数</th></tr>`,
		`qs.set("api_id", okxSelected)`,
		`qs.set("binance_api_id", binanceSelected)`,
		`function renderAnalysisAPISelect(select, exchange)`,
		`默认 ' + escapeHTML(exchangeLabel(exchange)) + ' 交易 API`,
		`id="analysis-symbol-section"`,
		`id="analysis-trade-history-section"`,
		`OKX 盈亏分析`,
		`Binance 盈亏分析`,
		`analysis-okx-symbol-status`,
		`analysis-binance-symbol-status`,
		`analysis-okx-rows`,
		`analysis-binance-rows`,
		`analysis-symbol-table`,
		`analysis-table-wrap`,
		`analysis-balance-rows`,
		`analysis-binance-balance-rows`,
		`balance-table-wrap`,
		`balanceRowsHTML`,
		`交易 API<select id="analysis-api-id"></select>`,
		`id="analysis-api-id"`,
		`$("analysis-api-id")`,
		`for (const key of ["eq_usd", "eq", "cash_bal", "avail_bal"])`,
		".balance-table-wrap {\n      height: 188px;",
	} {
		if strings.Contains(tvbotHTML, old) {
			t.Fatalf("tvbot ui should not include %s", old)
		}
	}
	analysisUpdated := strings.Index(tvbotHTML, `id="analysis-updated"`)
	windowToolbar := strings.Index(tvbotHTML, `class="balance-window-toolbar"`)
	analysisGrid := strings.Index(tvbotHTML, `class="analysis-balance-grid"`)
	if analysisUpdated < 0 || windowToolbar < 0 || analysisGrid < 0 {
		t.Fatal("analysis top layout markers are missing")
	}
	if !(analysisUpdated < windowToolbar && windowToolbar < analysisGrid) {
		t.Fatal("analysis should show order time and period switch before exchange columns")
	}
	okxOrder := strings.Index(tvbotHTML, `OKX 订单`)
	binanceOrder := strings.Index(tvbotHTML, `Binance 订单`)
	okxValuation := strings.Index(tvbotHTML, `id="analysis-usdt-eq"`)
	binanceValuation := strings.Index(tvbotHTML, `id="analysis-binance-usdt-eq"`)
	okxChart := strings.Index(tvbotHTML, `id="usdt-chart" class="mini-usdt-chart"`)
	binanceChart := strings.Index(tvbotHTML, `id="analysis-binance-usdt-chart" class="mini-usdt-chart"`)
	okxPNL := strings.Index(tvbotHTML, `id="analysis-okx-net-pnl"`)
	binancePNL := strings.Index(tvbotHTML, `id="analysis-binance-net-pnl"`)
	if okxOrder < 0 || binanceOrder < 0 || okxValuation < 0 || binanceValuation < 0 || okxChart < 0 || binanceChart < 0 || okxPNL < 0 || binancePNL < 0 {
		t.Fatal("analysis layout markers are missing")
	}
	if !(okxOrder < binanceOrder) {
		t.Fatal("OKX and Binance analysis panels should remain in stable order")
	}
	if !(okxValuation < okxPNL && okxPNL < okxChart) {
		t.Fatal("OKX column should show USDT valuation cards, pnl summary, then equity chart")
	}
	if !(binanceValuation < binancePNL && binancePNL < binanceChart) {
		t.Fatal("Binance column should show USDT valuation cards, pnl summary, then equity chart")
	}
	tradeHistory := strings.Index(tvbotHTML, `id="analysis-trade-history-title"`)
	tradeRows := strings.Index(tvbotHTML, `id="analysis-trade-rows"`)
	tradePage := strings.Index(tvbotHTML, `id="analysis-trade-page-info"`)
	if tradeHistory < 0 || tradeRows < 0 || tradePage < 0 || !(analysisGrid < tradeHistory && tradeHistory < tradeRows && tradeRows < tradePage) {
		t.Fatal("analysis should show paginated trade history after the active exchange summary")
	}
}
