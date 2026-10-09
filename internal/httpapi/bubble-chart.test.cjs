const { test } = require('node:test');
const assert = require('node:assert/strict');
const B = require('./bubble-chart.js');
const event = (id, at = 1000, price = 100, side = 'long', symbol = 'ETHUSDT') => ({ id: String(id), exchange: 'binance', symbol, event_time: new Date(at).toISOString(), price, position_side: side, notional_usd: 1, quantity: .01 });

test('candle range uses 10% padding, flat-price minimum span and valid candles only', () => {
  assert.deepEqual(B.candleRange([{ low: 90, high: 110 }]), [88, 112]);
  const flat = B.candleRange([{ low: 100, high: 100 }]);
  assert.ok(Math.abs(flat[0] - 99.94) < 1e-9 && Math.abs(flat[1] - 100.06) < 1e-9);
  assert.deepEqual(B.candleRange([{ low: NaN, high: 1 }, { low: 0, high: 0 }, { low: 2, high: 1 }]), null);
});

test('tiny events survive, IDs dedupe, symbols and invalid events cannot leak', () => {
  const original = event('a');
  const values = B.mergeEvents([original], [event('a'), event('b'), event('wrong', 1000, 100, 'short', 'BTCUSDT'), { ...event('bad'), price: 0 }, { ...event('other'), exchange: 'okx' }], 'ETHUSDT');
  assert.deepEqual(values.map(e => e.id), ['a', 'b']);
  assert.deepEqual(B.windowEvents(values, 1000, 1001), values);
  assert.equal(B.windowEvents(values, 0, 1000).length, 0);
});

test('timestamp placements need no candle bucket, offset in pixels, border circles and overlaps survive', () => {
  const events = [event('long', 2000), event('short', 2000, 100, 'short'), event('edge', 0, 90), event('outside', 1000, 120), event('same', 2000)];
  const result = B.placements(events, [90, 110], { left: 0, right: 100, top: 0, bottom: 100 }, at => at / 40, price => (110 - price) * 5);
  assert.equal(result.points.length, 4);
  assert.equal(result.outside, 1);
  assert.equal(result.points.find(p => p.event.id === 'long').y, 56);
  assert.equal(result.points.find(p => p.event.id === 'short').y, 44);
  const edge = result.points.find(p => p.event.id === 'edge');
  assert.ok(edge.x - edge.radius >= 0 && edge.y + edge.radius <= 100);
  assert.deepEqual(B.hitIndex(result.points)(50, 56).map(e => e.id).sort(), ['long', 'same']);
});

test('capped snapshot supplements more than 5000 events with fixed cutoff, page duplicates dedupe and completed coverage is reused', async () => {
  const all = Array.from({ length: 6003 }, (_, i) => event(i, 1000 + i));
  let merged = B.mergeEvents([], all.slice(-5000), 'ETHUSDT');
  const queries = [], states = [];
  const loader = new B.WindowLoader(async path => {
    const q = new URLSearchParams(path.split('?')[1]); queries.push(q);
    const index = Number(q.get('cursor') || 0);
    return { data: { rows: all.slice(index === 0 ? 0 : index - 1, index + 500), next_cursor: index + 500 < all.length ? String(index + 500) : '' } };
  }, rows => { merged = B.mergeEvents(merged, rows, 'ETHUSDT'); }, state => states.push(state), () => 9000);
  await loader.ensure('ETHUSDT', 1000, 10000);
  assert.equal(merged.length, 6003);
  assert.equal(states.at(-1).status, 'complete');
  assert.equal(states.at(-1).loaded, 6003);
  for (const q of queries) {
    assert.equal(q.get('to'), new Date(9000).toISOString());
    assert.equal(q.get('minimum'), '0'); assert.equal(q.get('limit'), '500');
    assert.equal(q.get('side'), 'all'); assert.equal(q.get('field'), 'notional_usd');
  }
  const count = queries.length;
  await loader.ensure('ETHUSDT', 1000, 10000);
  assert.equal(queries.length, count);
});

test('partial failure keeps rows, waits for retry and never marks incomplete range covered', async () => {
  let fail = true, requests = 0, rows = [];
  const loader = new B.WindowLoader(async path => {
    requests++;
    const q = new URLSearchParams(path.split('?')[1]);
    if (!q.has('cursor')) return { data: { rows: [event('first')], next_cursor: 'second' } };
    if (fail) throw new Error('offline');
    return { data: { rows: [event('last', 2000)], next_cursor: '' } };
  }, page => { rows = B.mergeEvents(rows, page, 'ETHUSDT'); }, () => {}, () => 3000);
  await loader.ensure('ETHUSDT', 0, 3000);
  assert.equal(loader.state.status, 'error'); assert.equal(rows.length, 1);
  await loader.ensure('ETHUSDT', 0, 3000);
  assert.equal(requests, 2);
  fail = false;
  await loader.ensure('ETHUSDT', 0, 3000, true);
  assert.equal(rows.length, 2); assert.equal(loader.state.status, 'complete');
});

test('switch/reset aborts old requests, late responses and states cannot leak into a new symbol', async () => {
  let resolveOld, oldSignal;
  const rows = [], states = [];
  const loader = new B.WindowLoader((path, options) => {
    if (path.includes('ETHUSDT')) { oldSignal = options.signal; return new Promise(resolve => { resolveOld = resolve; }); }
    return Promise.resolve({ data: { rows: [event('btc', 1000, 100, 'long', 'BTCUSDT')] } });
  }, page => rows.push(...page), state => states.push(state), () => 3000);
  const old = loader.ensure('ETHUSDT', 0, 3000);
  loader.reset();
  await loader.ensure('BTCUSDT', 0, 3000);
  resolveOld({ data: { rows: [event('old')], next_cursor: '' } });
  await old;
  assert.equal(oldSignal.aborted, true);
  assert.deepEqual(rows.map(e => e.id), ['btc']);
  assert.ok(states.at(-1).key.startsWith('BTCUSDT'));
});

test('coverage only fetches gaps, concurrent same-window calls share request, cursor cycles fail explicitly', async () => {
  let requests = 0;
  const loader = new B.WindowLoader(async path => {
    requests++;
    const q = new URLSearchParams(path.split('?')[1]);
    assert.equal(q.get('from'), new Date(1000).toISOString());
    assert.equal(q.get('to'), new Date(2000).toISOString());
    return { data: { rows: [], next_cursor: 'loop' } };
  }, () => {}, () => {}, () => 3000);
  loader.cover(0, 1000); loader.cover(2000, 3000);
  await Promise.all([loader.ensure('ETHUSDT', 0, 3000), loader.ensure('ETHUSDT', 0, 3000)]);
  assert.equal(requests, 2);
  assert.equal(loader.state.status, 'error');
  assert.match(loader.state.error, /游标/);
});
