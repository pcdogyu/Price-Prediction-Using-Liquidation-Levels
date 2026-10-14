const { test } = require('node:test');
const assert = require('node:assert/strict');
const K = require('./alerts-chart.js');

const candle = (at, open = 1, high = 2, low = 0.5, close = 1.5, volume = 1000) => ({ t: at, open, high, low, close, volume });
const signal = (id, at, kind = 'aggressive', side = 'long', price = 100) => ({ id, t: at, kind, side, reference_price: price });

test('only the nine requested periods are offered', () => {
  assert.deepEqual(K.INTERVALS, ['1m', '2m', '5m', '15m', '30m', '1h', '4h', '8h', '12h']);
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

test('volumeMax only looks at the visible candles', () => {
  assert.equal(K.volumeMax([candle(0, 1, 2, 1, 2, 4000), candle(5000, 1, 2, 1, 2, 9000)], { start: 0, end: 1000 }, 1000), 4000);
  assert.equal(K.volumeMax([candle(0, 1, 2, 1, 2, NaN)], { start: 0, end: 1000 }, 1000), 0);
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

test('filterSignals dedupes and honours the kind toggles', () => {
  const all = [signal('a', 1000), signal('a', 1000), signal('s', 2000, 'steady', 'short'), { id: 'bad', t: NaN }];
  assert.deepEqual(K.filterSignals(all, { aggressive: true, steady: true }).map(s => s.id), ['a', 's']);
  assert.deepEqual(K.filterSignals(all, { aggressive: false, steady: true }).map(s => s.id), ['s']);
  assert.deepEqual(K.filterSignals(all, { aggressive: true, steady: false }).map(s => s.id), ['a']);
  assert.deepEqual(K.filterSignals(all, { aggressive: false, steady: false }), []);
});

test('markerGroups buckets by candle, kind and direction with counts', () => {
  const groups = K.markerGroups([
    signal('a', 1500), signal('b', 1800), signal('c', 1500, 'steady'), signal('d', 1500, 'aggressive', 'short'),
    signal('e', 2500), { id: 'no-price', t: 5000, kind: 'steady', side: 'long' }
  ], 1000);
  assert.deepEqual(groups.map(g => [g.bucket, g.kind, g.side, g.count]),
    [[1000, 'aggressive', 'long', 2], [1000, 'steady', 'long', 1], [1000, 'aggressive', 'short', 1], [2000, 'aggressive', 'long', 1], [5000, 'steady', 'long', 1]]);
  assert.equal(groups[0].price, 100);
  assert.equal(groups[0].latest, 1800);
  assert.equal(groups[4].price, 0);
  assert.equal(K.bucketStart(2999, 1000), 2000);
});

test('oldestSignal reports the earliest fetched signal time', () => {
  assert.equal(K.oldestSignal([signal('a', 5000), signal('b', 3000)]), 3000);
  assert.equal(K.oldestSignal([]), Infinity);
});
