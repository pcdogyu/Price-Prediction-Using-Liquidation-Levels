const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const K = require('./alerts-chart.js');
const chartApp = fs.readFileSync(require.resolve('./alerts-chart-app.js'), 'utf8');

const candle = (at, open = 1, high = 2, low = 0.5, close = 1.5, volume = 1000) => ({ t: at, open, high, low, close, volume });
const signal = (id, at, kind = 'aggressive', side = 'long', price = 100) => ({ id, t: at, kind, side, reference_price: price });

test('position trade labels are standalone candle-anchored badges', () => {
  assert.doesNotMatch(chartApp, /ctx\.lineTo\(item\.labelX, item\.labelCenterY/);
  assert.match(chartApp, /vertically collision-aware/);
});

test('price-axis gestures stay independent from time-axis gestures', () => {
  assert.match(chartApp, /const inPriceAxis = at =>/);
  assert.match(chartApp, /const mode = inPriceAxis\(at\) \? 'price' : inPlot\(at\) \? 'chart'/);
  assert.match(chartApp, /if \(drag\.mode === 'chart'\) \{/);
  assert.match(chartApp, /event\.ctrlKey \|\| event\.shiftKey \|\| inPriceAxis\(at\)/);
});

test('the K-line uses the full price canvas and the lower panel is net long-short', () => {
  assert.doesNotMatch(chartApp, /volumeTop/);
  assert.doesNotMatch(chartApp, /volumeMax\(state\.candles/);
  assert.doesNotMatch(chartApp, /成交额/);
  assert.match(chartApp, /bottom: positionMode \? 42 : 18/);
  assert.match(chartApp, /净多空（空 − 多）/);
});

test('position chart tooltip distinguishes merged closing fills', () => {
  assert.match(chartApp, /marker\.phase === 'exit' \? '平仓' : '开仓'/);
});

test('position chart uses orange for short closes and blue for long closes', () => {
  assert.match(chartApp, /marker\.phase === 'exit'\s*\n?\s*\? \(marker\.side === 'short' \? colors\.orange : colors\.blue\)/);
});

test('the requested periods are offered', () => {
  assert.deepEqual(K.INTERVALS, ['1m', '2m', '3m', '5m', '15m', '30m', '1h', '4h', '8h', '12h']);
  assert.equal(K.intervalMs('3m'), 180000);
  assert.equal(K.intervalMs('4h'), 14400000);
  assert.equal(K.intervalMs('12h'), 43200000);
  assert.equal(K.intervalMs('13m'), 0);
});

test('fitWindow anchors the newest candle with a right margin', () => {
  assert.deepEqual(K.fitWindow(1500000, 1000, 10), { start: 1500000 + 3000 - 10000, end: 1500000 + 3000 });
  assert.equal(K.fitWindow(0, 60000, K.DEFAULT_VISIBLE).end, 3 * 60000);
});

test('zoomWindow keeps the cursor time fixed', () => {
  const limits = { minSpan: 10000, maxSpan: 100000, minStart: -50000, maxEnd: 200000 };
  const zoomed = K.zoomWindow({ start: 0, end: 40000 }, 20000, 2, limits);
  assert.equal(zoomed.end - zoomed.start, 80000);
  assert.equal((20000 - zoomed.start) / (zoomed.end - zoomed.start), .5);
});

test('zoomWindow clamps to the smallest and largest span', () => {
  const limits = { minSpan: 10000, maxSpan: 60000, minStart: -100000, maxEnd: 500000 };
  const narrow = K.zoomWindow({ start: 100000, end: 110000 }, 105000, .1, limits);
  assert.equal(narrow.end - narrow.start, 10000);
  const wide = K.zoomWindow({ start: 100000, end: 190000 }, 150000, 4, limits);
  assert.equal(wide.end - wide.start, 60000);
});

test('panWindow shifts time and cannot run past the loaded edges', () => {
  const limits = { minSpan: 10000, maxSpan: 60000, minStart: 0, maxEnd: 200000 };
  const view = { start: 20000, end: 60000 };
  assert.deepEqual(K.panWindow(view, 5000, limits), { start: 25000, end: 65000 });
  assert.deepEqual(K.panWindow(view, -40000, limits), { start: 0, end: 40000 });
  assert.deepEqual(K.panWindow(view, 400000, limits), { start: 160000, end: 200000 });
});

test('clampWindow keeps a span wider than the data anchored at the left edge', () => {
  const clamped = K.clampWindow({ start: -90000, end: 90000 }, { minSpan: 1000, maxSpan: 180000, minStart: 0, maxEnd: 50000 });
  assert.deepEqual(clamped, { start: 0, end: 180000 });
});

test('visibleCandles clips by candle span, not by start alone', () => {
  const candles = [candle(0), candle(1000), candle(2000), candle(3000)];
  // the candle that still covers the left edge of the window stays visible
  assert.deepEqual(K.visibleCandles(candles, { start: 500, end: 2500 }, 1000).map(c => c.t), [0, 1000, 2000]);
  assert.deepEqual(K.visibleCandles(candles, { start: 0, end: 1 }, 1000).map(c => c.t), [0]);
  assert.deepEqual(K.visibleCandles(candles, { start: 4000, end: 5000 }, 1000), []);
});

test('priceRange pads the visible extremes and keeps a flat series open', () => {
  const candles = [candle(0, 2, 10, 1, 9), candle(1000, 9, 12, 8, 10), candle(9000, 1, 900, 1, 2)];
  const [low, high] = K.priceRange(candles, { start: 0, end: 2000 }, 1000);
  assert.ok(Math.abs(low - (1 - (12 - 1) * .08)) < 1e-9, `low ${low}`);
  assert.ok(Math.abs(high - (12 + (12 - 1) * .08)) < 1e-9, `high ${high}`);
  const flat = K.priceRange([candle(0, 5, 5, 5, 5)], { start: 0, end: 1000 }, 1000);
  assert.ok(flat[1] > flat[0] && Math.abs((flat[0] + flat[1]) / 2 - 5) < 1e-6, JSON.stringify(flat));
  assert.equal(K.priceRange([candle(0, 1, 0, -1, -2)], { start: 0, end: 1000 }, 1000), null);
  assert.equal(K.priceRange([], { start: 0, end: 1000 }, 1000), null);
});

test('zoomPriceRange keeps the pointer price anchored while clamping the span', () => {
  const limits = { minSpan: 10, maxSpan: 200 };
  const zoomed = K.zoomPriceRange([100, 200], 125, .5, limits);
  assert.deepEqual(zoomed, [112.5, 162.5]);
  assert.equal((125 - zoomed[0]) / (zoomed[1] - zoomed[0]), .25);
  assert.deepEqual(K.zoomPriceRange([100, 200], 150, .01, limits), [145, 155]);
  assert.deepEqual(K.zoomPriceRange([100, 200], 150, 10, limits), [50, 250]);
  assert.equal(K.zoomPriceRange([100, 100], 100, 2, limits), null);
});

test('dragPriceRange zooms up or down around the initial price-axis pointer', () => {
  const limits = { minSpan: 10, maxSpan: 400 };
  const up = K.dragPriceRange([100, 200], 125, -50, 100, limits);
  const down = K.dragPriceRange([100, 200], 125, 50, 100, limits);
  assert.ok(up[1] - up[0] < 100);
  assert.ok(down[1] - down[0] > 100);
  assert.ok(Math.abs((125 - up[0]) / (up[1] - up[0]) - .25) < 1e-9);
  assert.ok(Math.abs((125 - down[0]) / (down[1] - down[0]) - .25) < 1e-9);
});

test('volumeMax only looks at the visible candles', () => {
  assert.equal(K.volumeMax([candle(0, 1, 2, 1, 2, 4000), candle(5000, 1, 2, 1, 2, 9000)], { start: 0, end: 1000 }, 1000), 4000);
  assert.equal(K.volumeMax([candle(0, 1, 2, 1, 2, NaN)], { start: 0, end: 1000 }, 1000), 0);
});

test('radarZone returns VWAP opening and close-only boundaries', () => {
  const candles = Array.from({ length: 4 }, (_, index) => candle(index * 60000, 100 + index, 101 + index, 99 + index, 100 + index, 1000));
  const zone = K.radarZone(candles, 4, .8, 1);
  assert.equal(zone.ready, true);
  assert.equal(zone.period_minutes, 4);
  assert.ok(zone.upper_open_price > zone.vwap);
  assert.ok(zone.lower_open_price < zone.vwap);
  assert.ok(zone.current_z_score > 0);
  assert.ok(zone.green_zone_upper < zone.red_zone_lower);
});

test('VWAP zone bounds apply the hard Donchian drawdown gates', () => {
  const bounds = K.vwapZoneBounds(84000, 500, .8, 84800, 81300, .022, .35);
  assert.equal(bounds.green_zone_upper, 82525);
  assert.equal(bounds.red_zone_lower, 84400);
  assert.equal(bounds.boundary_overlap, false);
});

test('VWAP signal filtering uses both effective opening boundaries', () => {
  const zone = { green_zone_upper: 82525, red_zone_lower: 83575, boundary_overlap: false, current_price: 83000 };
  assert.equal(K.vwapSignalAllowed(signal('green-high', 1000, 'aggressive', 'long', 83300), zone), false);
  assert.equal(K.vwapSignalAllowed(signal('green-low', 1000, 'steady', 'long', 82050), zone), true);
  assert.equal(K.vwapSignalAllowed(signal('red-low', 1000, 'aggressive', 'short', 83300), zone), false);
  assert.equal(K.vwapSignalAllowed(signal('red-high', 1000, 'steady', 'short', 84144), zone), true);
  assert.equal(K.vwapSignalAllowed(signal('overlap', 1000, 'steady', 'long', 82050), { ...zone, boundary_overlap: true }), false);
});

test('niceTicks stays inside the range and terminates', () => {
  assert.deepEqual(K.niceTicks(0, 10, 5), [0, 2, 4, 6, 8, 10]);
  assert.deepEqual(K.niceTicks(5, 5, 5), []);
  const tiny = K.niceTicks(0.0001, 0.0009, 4);
  assert.ok(tiny.length > 0 && tiny.every(value => value >= 0.0001 && value <= 0.0009), JSON.stringify(tiny));
});

test('mergeCandles dedupes by time with the newer arrival winning', () => {
  const merged = K.mergeCandles([candle(1000, 1, 1, 1, 1), candle(2000, 1, 1, 1, 1)], [candle(2000, 5, 5, 5, 5), candle(0, 2, 2, 2, 2)]);
  assert.deepEqual(merged.map(c => c.t), [0, 1000, 2000]);
  assert.equal(merged[2].open, 5);
});

test('normaliseCandles converts API rows and drops unusable ones', () => {
  const rows = [
    { time: new Date(1000).toISOString(), open: 1, high: 2, low: 0.5, close: 1.5, volume_usd: 500 },
    { time: 'not a date', open: 1, high: 1, low: 1, close: 1 },
    { time: new Date(2000).toISOString(), open: NaN, high: 1, low: 1, close: 1 },
    { time: new Date(3000).toISOString(), open: 1, high: 1, low: 1, close: 1 }
  ];
  const candles = K.normaliseCandles(rows);
  assert.deepEqual(candles.map(c => c.t), [1000, 3000]);
  assert.equal(candles[0].volume, 500);
  assert.equal(candles[1].volume, 0);
  assert.deepEqual(K.normaliseCandles(null), []);
});

test('normaliseLiquidationDelta keeps signed short-minus-long buckets ordered', () => {
  const rows = [
    { time: new Date(120000).toISOString(), long_usd: 800, short_usd: 300, delta_usd: -500, count: 4 },
    { time: new Date(0).toISOString(), long_usd: 100, short_usd: 250, count: 2 },
    { time: 'bad', long_usd: 1, short_usd: 1 },
    { time: new Date(180000).toISOString(), long_usd: -1, short_usd: 1 }
  ];
  const points = K.normaliseLiquidationDelta(rows);
  assert.deepEqual(points.map(point => [point.t, point.delta_usd, point.count]), [[0, 150, 2], [120000, -500, 4]]);
  assert.deepEqual(K.normaliseLiquidationDelta(null), []);
});

test('filterSignals dedupes and honours the kind toggles', () => {
  const all = [signal('a', 1000), signal('a', 1000), signal('s', 2000, 'steady', 'short'), { id: 'bad', t: NaN }];
  assert.deepEqual(K.filterSignals(all, { aggressive: true, steady: true }).map(s => s.id), ['a', 's']);
  assert.deepEqual(K.filterSignals(all, { aggressive: false, steady: true }).map(s => s.id), ['s']);
  assert.deepEqual(K.filterSignals(all, { aggressive: true, steady: false }).map(s => s.id), ['a']);
  assert.deepEqual(K.filterSignals(all, { aggressive: false, steady: false }), []);
});

test('markerGroups buckets by candle, kind and direction with counts and amounts', () => {
  const groups = K.markerGroups([
    Object.assign(signal('a', 1500), { alert_notional_usd: 10 }),
    Object.assign(signal('b', 1800), { alert_notional_usd: 20 }),
    Object.assign(signal('c', 1500, 'steady'), { alert_notional_usd: 30 }),
    Object.assign(signal('d', 1500, 'aggressive', 'short'), { alert_notional_usd: 40 }),
    signal('e', 2500), { id: 'no-price', t: 5000, kind: 'steady', side: 'long' }
  ], 1000);
  assert.deepEqual(groups.map(g => [g.bucket, g.kind, g.side, g.count]),
    [[1000, 'aggressive', 'long', 2], [1000, 'steady', 'long', 1], [1000, 'aggressive', 'short', 1], [2000, 'aggressive', 'long', 1], [5000, 'steady', 'long', 1]]);
  assert.equal(groups[0].price, 100);
  assert.equal(groups[0].latest, 1800);
  assert.equal(groups[0].notionalUSD, 30);
  assert.equal(groups[1].notionalUSD, 30);
  assert.equal(groups[2].notionalUSD, 40);
  assert.equal(groups[4].price, 0);
  assert.equal(groups[4].notionalUSD, 0);
  assert.equal(K.bucketStart(2999, 1000), 2000);
});

test('oldestSignal reports the earliest fetched signal time', () => {
  assert.equal(K.oldestSignal([signal('a', 5000), signal('b', 3000)]), 3000);
  assert.equal(K.oldestSignal([]), Infinity);
});

test('filterAlerts dedupes alerts and uses trade direction for chart markers', () => {
  const all = [
    { id: 'a', t: 1000, side: 'long', trade_side: 'short' },
    { id: 'a', t: 1000, side: 'long', trade_side: 'short' },
    { id: 'b', t: 2000, side: 'short', trade_side: 'short' },
    { id: 'bad', t: NaN }
  ];
  assert.deepEqual(K.filterAlerts(all).map(row => [row.id, row.side]), [['a', 'short'], ['b', 'short']]);
});

test('alertGroups keeps long and short warnings separate and aggregates their amount', () => {
  const groups = K.alertGroups([
    { id: 'a', t: 1500, side: 'long', liquidation_side: 'long', reference_price: 100, notional_usd: 10 },
    { id: 'b', t: 1800, side: 'long', liquidation_side: 'long', reference_price: 101, notional_usd: 20 },
    { id: 'c', t: 1500, side: 'short', liquidation_side: 'short', reference_price: 99, notional_usd: 5 }
  ], 1000);
  assert.deepEqual(groups.map(group => [group.bucket, group.side, group.count, group.notionalUSD]), [
    [1000, 'long', 2, 30], [1000, 'short', 1, 5]
  ]);
  assert.equal(groups[0].price, 101);
  assert.equal(groups[0].type, 'alert');
});
