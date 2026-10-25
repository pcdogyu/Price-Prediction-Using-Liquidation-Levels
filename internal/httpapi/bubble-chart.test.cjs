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

test('Gate contract symbols normalize to Binance USDT symbols', () => {
  assert.equal(B.normalizeMarketSymbol('BTC_USDT'), 'BTCUSDT');
  assert.equal(B.normalizeMarketSymbol('BTC-USDT-SWAP'), 'BTCUSDT');
  assert.equal(B.normalizeMarketSymbol('gate:ETH_USDT'), 'ETHUSDT');
  assert.equal(B.normalizeMarketSymbol('BTCUSDC'), '');
  assert.equal(B.normalizeMarketSymbol('not-a-symbol'), '');
});

test('trade markers map long and short entry/exit actions and keep the selected row', () => {
  const markers = B.normalizeTradeMarkers([
    { row_key: 'long-row', exchange: 'binance', inst_id: 'BTC_USDT', side: 'long', entry_time: '2026-01-01T00:00:00Z', entry_px: '100', exit_time: '2026-01-01T01:00:00Z', exit_px: '110' },
    { row_key: 'short-row', exchange: 'binance', inst_id: 'BTCUSDT', side: 'short', entry_time: '2026-01-01T02:00:00Z', entry_px: 120, exit_time: '2026-01-01T03:00:00Z', exit_px: 105 }
  ], 'BTCUSDT', 'short-row');
  assert.deepEqual(markers.map(item => [item.row_key, item.phase, item.action, item.selected]), [
    ['long-row', 'entry', 'buy', false],
    ['long-row', 'exit', 'sell', false],
    ['short-row', 'entry', 'sell', true],
    ['short-row', 'exit', 'buy', true]
  ]);
  assert.equal(markers[0].price, 100);
  assert.equal(markers[0].time, Date.parse('2026-01-01T00:00:00Z'));
  assert.equal(markers[0].exchange, 'binance');
});

test('trade markers preserve take-profit, stop-loss and manual/automatic labels', () => {
  const markers = B.normalizeTradeMarkers([
    { row_key: 'tp', inst_id: 'ETHUSDT', side: 'long', operation: 'take_profit', operation_source: 'auto', entry_time: '', entry_px: '', exit_time: '2026-01-01T01:00:00Z', exit_px: 2100 },
    { row_key: 'sl', inst_id: 'ETHUSDT', side: 'short', operation: 'stop_loss', operation_source: 'manual', entry_time: '', entry_px: '', exit_time: '2026-01-01T02:00:00Z', exit_px: 2200 }
  ], 'ETHUSDT');
  const layout = B.tradeMarkerPlacements(markers, [1900, 2300], { start: 0, end: Date.parse('2026-01-01T03:00:00Z'), x: () => 150 }, price => (2300 - price) / 2, { left: 0, right: 300, top: 0, bottom: 300 });
  assert.deepEqual(markers.map(item => [item.operation, item.operation_source]), [['take_profit', 'auto'], ['stop_loss', 'manual']]);
  assert.equal(layout.markers[0].labelText, '多平');
  assert.equal(layout.markers[1].labelText, '空平');
});

test('collapseTradeMarkers shows one opening or closing action per direction and candle', () => {
  const markers = B.normalizeTradeMarkers([
    { row_key: 'long-1', inst_id: 'ETHUSDT', side: 'long', operation_source: 'auto', entry_time: '2026-01-01T00:01:00Z', entry_px: 2000 },
    { row_key: 'long-2', inst_id: 'ETHUSDT', side: 'long', operation_source: 'auto', entry_time: '2026-01-01T00:04:00Z', entry_px: 2002 },
    { row_key: 'short-1', inst_id: 'ETHUSDT', side: 'short', operation_source: 'manual', entry_time: '2026-01-01T00:04:30Z', entry_px: 2001 },
    { row_key: 'long-3', inst_id: 'ETHUSDT', side: 'long', operation_source: 'auto', entry_time: '2026-01-01T00:06:00Z', entry_px: 2003 },
    { row_key: 'long-exit', inst_id: 'ETHUSDT', side: 'long', operation: 'take_profit', operation_source: 'auto', exit_time: '2026-01-01T00:04:00Z', exit_px: 2010 },
    { row_key: 'long-exit-2', inst_id: 'ETHUSDT', side: 'long', operation: 'close', operation_source: 'exchange', exit_time: '2026-01-01T00:04:30Z', exit_px: 2011 },
    { row_key: 'long-exit-3', inst_id: 'ETHUSDT', side: 'long', operation: 'stop_loss', operation_source: 'exchange', exit_time: '2026-01-01T00:04:45Z', exit_px: 2009 }
  ], 'ETHUSDT', 'long-2');
  const collapsed = B.collapseTradeMarkers(markers, 5 * 60 * 1000);
  assert.deepEqual(collapsed.map(item => [item.row_key, item.phase, item.side, item.collapsed_count]), [
    ['long-1', 'entry', 'long', 2],
    ['long-exit', 'exit', 'long', 3],
    ['short-1', 'entry', 'short', 1],
    ['long-3', 'entry', 'long', 1]
  ]);
  assert.equal(collapsed[0].selected, true);
  assert.equal(collapsed[1].operation, 'close');
  assert.equal(collapsed[1].operation_source, '');
  const layout = B.tradeMarkerPlacements([collapsed[1]], [1900, 2300], { start: 0, end: Date.parse('2026-01-01T01:00:00Z'), x: () => 150 }, () => 100, { left: 0, right: 300, top: 0, bottom: 300 });
  assert.equal(layout.markers[0].labelText, '多平×3');
  const shortLayout = B.tradeMarkerPlacements([{ ...collapsed[1], side: 'short' }], [1900, 2300], { start: 0, end: Date.parse('2026-01-01T01:00:00Z'), x: () => 150 }, () => 100, { left: 0, right: 300, top: 0, bottom: 300 });
  assert.equal(shortLayout.markers[0].labelText, '空平×3');
});

test('opening markers use B below long candles and S above short candles', () => {
  const firstAt = Date.parse('2026-01-01T00:00:00Z');
  const markers = B.normalizeTradeMarkers([
    { row_key: 'long-entry', inst_id: 'ETHUSDT', side: 'long', entry_time: firstAt, entry_px: 2000 },
    { row_key: 'short-entry', inst_id: 'ETHUSDT', side: 'short', entry_time: '2026-01-01T01:00:00Z', entry_px: 2100 }
  ], 'ETHUSDT');
  const layout = B.tradeMarkerPlacements(markers, [1900, 2200], { start: 0, end: Date.parse('2026-01-01T02:00:00Z'), x: at => 50 + (at - firstAt) / 3600000 * 100 }, price => (2200 - price) / 2, { left: 0, right: 300, top: 0, bottom: 300 });
  assert.deepEqual(layout.markers.map(item => [item.labelText, item.labelBelow, item.labelX]), [['B', true, 50], ['S', false, 150]]);
});

test('tradeMarkerPlacements separates labels that share one candle and price', () => {
  const markers = [
    { id: 'long-entry', time: 1000, price: 100, side: 'long', phase: 'entry' },
    { id: 'short-exit', time: 1000, price: 100, side: 'short', phase: 'exit' },
    { id: 'short-entry', time: 1000, price: 100, side: 'short', phase: 'entry' },
    { id: 'long-exit', time: 1000, price: 100, side: 'long', phase: 'exit' }
  ];
  const layout = B.tradeMarkerPlacements(markers, [90, 110], { start: 0, end: 2000, x: () => 150 }, () => 150, { left: 0, right: 300, top: 0, bottom: 300 });
  assert.equal(layout.visible, 4);
  assert.equal(layout.outside, 0);
  assert.deepEqual(Object.fromEntries(layout.markers.map(item => [item.marker.id, item.labelBelow])), {
    'long-entry': true,
    'short-exit': true,
    'short-entry': false,
    'long-exit': false
  });
  for (let i = 0; i < layout.markers.length; i++) {
    for (let j = i + 1; j < layout.markers.length; j++) {
      const a = layout.markers[i].box;
      const b = layout.markers[j].box;
      assert.ok(a.right <= b.left || b.right <= a.left || a.bottom <= b.top || b.bottom <= a.top, `${layout.markers[i].marker.id} overlaps ${layout.markers[j].marker.id}`);
    }
  }
});

test('trade marker normalization omits incomplete or invalid values without dropping valid points', () => {
  const markers = B.normalizeTradeMarkers([
    { row_key: 'valid', inst_id: 'ETH_USDT', side: 'long', entry_time: '1704067200000', entry_px: '2000', exit_time: '', exit_px: 2100 },
    { row_key: 'bad-time', inst_id: 'ETH_USDT', side: 'long', entry_time: 'later', entry_px: 2000, exit_time: '2026-01-01T00:00:00Z', exit_px: 2100 },
    { row_key: 'zero-time', inst_id: 'ETH_USDT', side: 'long', entry_time: '0001-01-01T00:00:00Z', entry_px: 2000, exit_time: '', exit_px: 2100 },
    { row_key: 'bad-price', inst_id: 'ETH_USDT', side: 'short', entry_time: '2026-01-01T00:00:00Z', entry_px: 0, exit_time: '2026-01-01T01:00:00Z', exit_px: 'bad' },
    { row_key: 'wrong-symbol', inst_id: 'BTC_USDT', side: 'long', entry_time: '2026-01-01T00:00:00Z', entry_px: 1, exit_time: '2026-01-01T01:00:00Z', exit_px: 2 },
    { row_key: 'wrong-side', inst_id: 'ETH_USDT', side: 'buy', entry_time: '2026-01-01T00:00:00Z', entry_px: 1, exit_time: '2026-01-01T01:00:00Z', exit_px: 2 }
  ], 'ETHUSDT');
  assert.deepEqual(markers.map(item => [item.row_key, item.phase]), [['valid', 'entry'], ['bad-time', 'exit']]);
});

test('trade markers retain order and fill details for K-line tooltips', () => {
  const markers = B.normalizeTradeMarkers([
    {
      row_key: 'doge-open',
      inst_id: 'DOGE_USDT',
      side: 'long',
      position_effect: 'open',
      entry_time: '2026-10-22T09:38:52Z',
      entry_px: '0.08076',
      ord_id: 'gate-order-1',
      trade_id: 'gate-trade-1',
      fill_sz: '963',
      turnover: '77.77',
      margin: '7.777',
      fee: '-0.0777',
      fee_ccy: 'USDT'
    }
  ], 'DOGEUSDT');
  assert.equal(markers.length, 1);
  assert.equal(markers[0].order_id, 'gate-order-1');
  assert.equal(markers[0].trade_id, 'gate-trade-1');
  assert.equal(markers[0].quantity, '963');
  assert.equal(markers[0].turnover, '77.77');
  assert.equal(markers[0].notional_usdt, '77.77');
  assert.equal(markers[0].margin_usdt, '7.777');
  assert.equal(markers[0].fee, '-0.0777');
});

test('timestamp placements apply independent long-down and short-up offsets', () => {
  const events = [event('long', 2000), event('short', 2000, 100, 'short'), event('edge', 0, 90), event('outside', 1000, 120), event('same', 2000)];
  const result = B.placements(events, [90, 110], { left: 0, right: 100, top: 0, bottom: 100 }, at => at / 40, price => (110 - price) * 5, { long: 20, short: 20 });
  assert.equal(result.points.length, 4);
  assert.equal(result.outside, 1);
  assert.equal(result.points.find(p => p.event.id === 'long').y, 70);
  assert.equal(result.points.find(p => p.event.id === 'short').y, 30);
  const edge = result.points.find(p => p.event.id === 'edge');
  assert.ok(edge.x - edge.radius >= 0 && edge.y + edge.radius <= 100);
  assert.deepEqual(B.hitIndex(result.points)(50, 70).map(e => e.id).sort(), ['long', 'same']);
});

test('time grid ticks align to every half hour and exclude the range end', () => {
  const halfHour = 30 * 60 * 1000;
  const start = Date.parse('2026-01-01T00:07:00Z');
  const end = Date.parse('2026-01-01T02:00:00Z');
  assert.deepEqual(B.timeGridTicks(start, end, halfHour), [
    Date.parse('2026-01-01T00:30:00Z'),
    Date.parse('2026-01-01T01:00:00Z'),
    Date.parse('2026-01-01T01:30:00Z')
  ]);
  assert.deepEqual(B.timeGridTicks(end, start, halfHour), []);
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
