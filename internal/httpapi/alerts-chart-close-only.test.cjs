const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const K = require('./alerts-chart.js');
const chartApp = fs.readFileSync(require.resolve('./alerts-chart-app.js'), 'utf8');
const chartHTML = fs.readFileSync(require.resolve('./alerts-chart.html'), 'utf8');

test('the chart marks close-only records without changing opening-zone filtering', () => {
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: false, close_allowed: true }), true);
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: true, close_allowed: true }), false);
  assert.equal(K.closeOnlyIgnored({ filter_applied: true, open_allowed: false, close_allowed: false }), false);
  assert.match(chartApp, /kline-close-only-label/);
  assert.match(chartApp, /closeOnlyPrefix/);
  assert.match(chartHTML, /仅平仓区忽略/);
});
