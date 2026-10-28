const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const K = require('./alerts-chart.js');
const chartApp = fs.readFileSync(require.resolve('./alerts-chart-app.js'), 'utf8');
const chartHTML = fs.readFileSync(require.resolve('./alerts-chart.html'), 'utf8');

test('the chart uses an independent Donchian filter switch for close-only signals', () => {
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: false, close_allowed: true }), true);
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: true, close_allowed: true }), false);
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: false, close_allowed: false }), false);
  assert.match(chartApp, /K\.filterAlerts\(state\.alerts\)\.filter\(donchianAllowsSignal\)/);
  assert.match(chartApp, /if \(!state\.toggles\.donchian\) return true/);
  assert.match(chartApp, /STORE_DONCHIAN/);
  assert.match(chartApp, /function renderSignalStatus\(\)/);
  assert.match(chartApp, /if \(toggle === 'donchian' && state\.signals\.length\) renderSignalStatus\(\)/);
  assert.match(chartApp, /state\.toggles\.donchian && K\.closeOnlyIgnored/);
  assert.match(chartApp, /Object\.assign\(\{\}, signal, \{ filter_applied: false \}\)/);
  assert.match(chartApp, /kline-close-only-label/);
  assert.match(chartApp, /平仓区域预警信号过滤/);
  assert.match(chartApp, /稳健信号过滤/);
  assert.match(chartApp, /激进信号过滤/);
  assert.match(chartHTML, /平仓区域信号过滤/);
  assert.match(chartHTML, /过滤掉的下单信号仍会保留/);
  assert.match(chartHTML, /id="kline-donchian"/);
  assert.match(chartHTML, /VWAP 颜色填充开关只控制图中的背景填色，不负责 Donchian 信号过滤/);
});

test('turning off VWAP fill keeps the historical boundary lines and labels', () => {
  assert.match(chartApp, /if \(!state\.toggles\.zones\) return;/);
  assert.match(chartApp, /historyLine\('red_zone_lower'/);
  assert.match(chartApp, /historyLine\('green_zone_upper'/);
  assert.match(chartApp, /historyLine\('vwap'/);
  assert.match(chartApp, /label\('开仓区 · 高位开空'/);
  assert.match(chartApp, /label\('仅平仓区'/);
});
