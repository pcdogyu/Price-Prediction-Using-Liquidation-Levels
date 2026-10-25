const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const K = require('./alerts-chart.js');
const chartApp = fs.readFileSync(require.resolve('./alerts-chart-app.js'), 'utf8');
const chartHTML = fs.readFileSync(require.resolve('./alerts-chart.html'), 'utf8');

test('the chart hides close-only warnings when VWAP filtering is enabled', () => {
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: false, close_allowed: true }), true);
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: true, close_allowed: true }), false);
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: false, close_allowed: false }), false);
  assert.match(chartApp, /K\.filterAlerts\(state\.alerts\)\.filter\(vwapAllowsSignal\)/);
  assert.match(chartApp, /kline-close-only-label/);
  assert.match(chartApp, /平仓区域预警信号过滤/);
  assert.match(chartApp, /稳健信号过滤/);
  assert.match(chartApp, /激进信号过滤/);
  assert.match(chartHTML, /平仓区域信号过滤/);
  assert.match(chartHTML, /平仓区不绘制预警信息/);
});
