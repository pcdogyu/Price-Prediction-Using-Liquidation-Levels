'use strict';

const NS = 'http://www.w3.org/2000/svg';
const paint = value => window.LiquidationTheme.color(value);
const $ = id => document.getElementById(id);
const cache = { signal: null, map: null, market: null, volume: null };
let savedInterval = '15m';
try { savedInterval = localStorage.getItem('liquidation.interval') || savedInterval; } catch (_) { /* browser storage is optional */ }
const defaultLiquidationMinimumUSD = 10000;
const chart = {
  interval: savedInterval,
  candles: [], signals: [], liquidations: [], liquidationsTruncated: false, liquidationMinimumUSD: defaultLiquidationMinimumUSD, latestPriceTradeID: 0, latestPriceTick: null, windowEnd: 0, visibleCount: 120,
  hasMore: false, nextBefore: '', loadingOlder: false,
  yLow: null, yHigh: null, autoSpan: null, yManual: false,
  atLatest: true, newData: false, drag: null
};
let symbol = 'ETHUSDT';
let refreshing = false;
let refreshSequence = 0;
let streamError = '';
let lastFailures = [];
let logCursor = '';
let logEntries = [];
let captureStateHoldUntil = 0;

const api = path => new URL('api/v1/' + path, document.baseURI).toString();
const finite = value => typeof value === 'number' && Number.isFinite(value);
const money = value => finite(value) ? Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value) : '—';
const compact = value => finite(value) ? Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 2 }).format(value) : '—';
const pct = value => finite(value) ? (value * 100).toFixed(1) + '%' : '—';
const when = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const patternName = { doji: '十字星', hammer: '锤头', shooting_star: '流星', bullish_engulfing: '看涨吞没', bearish_engulfing: '看跌吞没' };
const statusName = { unavailable: '不可用', armed: '等待触发', approaching: '接近触发', triggered: '已触发', expired: '已过期' };
const intervalName = { '1m': '1分钟', '2m': '2分钟', '3m': '3分钟', '5m': '5分钟', '10m': '10分钟', '15m': '15分钟', '30m': '30分钟', '1h': '1小时', '4h': '4小时', '8h': '8小时', '12h': '12小时', '24h': '24小时' };
const intervalMilliseconds = { '1m': 60000, '2m': 120000, '3m': 180000, '5m': 300000, '10m': 600000, '15m': 900000, '30m': 1800000, '1h': 3600000, '4h': 14400000, '8h': 28800000, '12h': 43200000, '24h': 86400000 };
const liquidationPriceOffsetUSD = 5;
const liquidationWallWidthScale = .9;
const liquidationWallRight = 1510;
const liquidationWallWidth = Math.round((liquidationWallRight - 1315) * liquidationWallWidthScale);
const dims = { height: 560, width: 1740, left: 72, top: 24, bottom: 42, candleRight: 1025, volumeLeft: 1060, volumeRight: 1280, liquidationLeft: liquidationWallRight - liquidationWallWidth, profileRight: liquidationWallRight, axisPriceRight: 1584, rankLabelLeft: 1614, rankLabelRight: 1728 };
if (!intervalName[chart.interval]) chart.interval = '15m';

function svgNode(tag, attrs = {}, text = '') {
  const element = document.createElementNS(NS, tag);
  Object.entries(attrs).forEach(([key, value]) => element.setAttribute(key, (key === 'fill' || key === 'stroke') && typeof value === 'string' ? paint(value) : value));
  if (text) element.textContent = text;
  return element;
}

async function json(path) {
  const response = await fetch(api(path), { cache: 'no-store', credentials: 'same-origin' });
  if (response.status === 401) {
    location.reload();
    throw new Error('登录已过期');
  }
  let body = null;
  try { body = await response.json(); } catch (_) { /* handled below */ }
  if (!response.ok) throw new Error('HTTP ' + response.status + ' · ' + (body?.detail || response.statusText || '请求失败'));
  return body;
}

function marketPath(before = '') {
  const params = new URLSearchParams({ symbol, interval: chart.interval, limit: String(chart.visibleCount) });
  if (before) params.set('before', before);
  return 'market?' + params.toString();
}

function captureCountdown(value) {
  const seconds = Math.max(0, Math.ceil(value / 1000));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  const remainder = seconds % 60;
  const clock = [minutes, remainder].map(item => String(item).padStart(2, '0')).join(':');
  return hours ? String(hours).padStart(2, '0') + ':' + clock : clock;
}

function updateCoinGlassCountdown() {
  if (Date.now() < captureStateHoldUntil) return;
  const schedule = cache.schedule;
  const intervalSeconds = Number(schedule?.interval_seconds);
  const nextAt = new Date(schedule?.next_capture_at).getTime();
  const serverAt = new Date(schedule?.server_time).getTime();
  if (!Number.isFinite(intervalSeconds) || intervalSeconds <= 0 || !Number.isFinite(nextAt) || !Number.isFinite(serverAt)) return;
  const receivedAt = Number(schedule.received_at_ms) || Date.now();
  const remaining = nextAt - serverAt - Math.max(0, Date.now() - receivedAt);
  const intervalText = intervalSeconds % 60 === 0 ? intervalSeconds / 60 + ' 分钟' : intervalSeconds + ' 秒';
  const state = $('coinglass-capture-state');
  state.classList.remove('error');
  state.textContent = '自动抓取：每 ' + intervalText + ' · 下次 ' + when(schedule.next_capture_at) + ' · 倒计时 ' + captureCountdown(remaining);
}

function updateTicker(view) {
  const summary = view?.summary;
  if (!summary) return;
  $('ticker-symbol').textContent = (view.symbol || symbol) + ' · Binance 永续价格';
  $('ticker-price').textContent = money(summary.last_price);
  const updatedAt = new Date(summary.updated_at).getTime();
  const ageSeconds = Number.isFinite(updatedAt) ? Math.max(0, (Date.now() - updatedAt) / 1000) : NaN;
  const freshness = Number.isFinite(ageSeconds) ? (ageSeconds <= 3 ? ' · 实时' : ' · 延迟 ' + Math.round(ageSeconds) + '秒') : '';
  $('ticker-updated').textContent = '更新 ' + when(summary.updated_at) + freshness + (view.backfill_complete ? '' : ' · 180天历史回填中');
  const change = finite(summary.change_pct_24h) ? summary.change_pct_24h : 0;
  const up = change >= 0;
  $('ticker-change').textContent = (up ? '+' : '') + change.toFixed(2) + '%';
  $('ticker-change').className = 'value ' + (up ? 'positive' : 'negative');
  $('ticker-change-amount').textContent = (summary.change_24h >= 0 ? '+' : '') + money(summary.change_24h);
  $('ticker-high').textContent = money(summary.high_24h);
  $('ticker-low').textContent = money(summary.low_24h);
  $('ticker-volume').textContent = '$' + compact(summary.volume_24h_usd);
  $('ticker-coverage').textContent = 'Binance USDⓈ-M · ' + (view.realtime_price ? 'aggTrade 实时' : 'REST 校准');
}

function updateProbabilities(probabilities) {
  [['up', 'upper_first'], ['down', 'lower_first'], ['none', 'neither']].forEach(([id, key]) => {
    const value = probabilities?.[key];
    $('p-' + id).textContent = pct(value);
    $('b-' + id).style.width = ((finite(value) ? value : 0) * 100) + '%';
  });
}

function updateTrigger(prefix, trigger) {
  const status = trigger?.status || 'unavailable';
  $(prefix + '-status').textContent = statusName[status] || status;
  $(prefix + '-status').style.borderColor = status === 'triggered' ? 'var(--buy)' : status === 'approaching' ? 'var(--warning)' : 'var(--strong-line)';
  $(prefix + '-target').textContent = finite(trigger?.target_price) && trigger.target_price !== 0 ? money(trigger.target_price) : '—';
  $(prefix + '-distance').textContent = finite(trigger?.distance_price) && finite(trigger?.distance_percent) ? money(trigger.distance_price) + ' · ' + trigger.distance_percent.toFixed(2) + '%' : '—';
  $(prefix + '-atr').textContent = finite(trigger?.distance_atr) ? trigger.distance_atr.toFixed(2) + ' ATR' : '—';
  $(prefix + '-prob').textContent = pct(trigger?.probability);
  $(prefix + '-time').textContent = trigger?.triggered_at ? when(trigger.triggered_at) : trigger?.expires_at ? '截止 ' + when(trigger.expires_at) : '—';
}

function mergeCandles(current, incoming) {
  const values = new Map(current.map(item => [new Date(item.time).getTime(), item]));
  incoming.forEach(item => values.set(new Date(item.time).getTime(), item));
  return Array.from(values.values()).sort((a, b) => new Date(a.time) - new Date(b.time));
}

function mergeSignals(current, incoming) {
  const key = item => new Date(item.time).getTime() + ':' + item.side;
  const values = new Map(current.map(item => [key(item), item]));
  incoming.forEach(item => values.set(key(item), item));
  return Array.from(values.values()).sort((a, b) => new Date(a.time) - new Date(b.time));
}

function mergeLiquidations(current, incoming) {
  const eligible = item => item?.id && item.exchange === 'binance' && finite(item.notional_usd) && item.notional_usd >= chart.liquidationMinimumUSD;
  const values = new Map(current.filter(eligible).map(item => [item.id, item]));
  incoming.forEach(item => { if (eligible(item)) values.set(item.id, item); });
  return Array.from(values.values()).sort((a, b) => new Date(a.event_time) - new Date(b.event_time));
}

function mergeMarket(view, older, reset) {
  const incomingCandles = view.candles || [];
  const incomingSignals = view.model_signals || [];
  const incomingLiquidations = view.liquidations || [];
  chart.liquidationMinimumUSD = finite(view.liquidation_minimum_usd) ? Math.max(0, view.liquidation_minimum_usd) : defaultLiquidationMinimumUSD;
  if (reset) {
    chart.candles = incomingCandles.slice();
    chart.signals = incomingSignals.slice();
    chart.liquidations = mergeLiquidations([], incomingLiquidations);
    chart.liquidationsTruncated = !!view.liquidations_truncated;
    chart.windowEnd = chart.candles.length;
    chart.atLatest = true;
    chart.newData = false;
    chart.yManual = false;
    chart.yLow = chart.yHigh = chart.autoSpan = null;
  } else if (older) {
    const oldFirst = chart.candles[0]?.time;
    const oldEnd = chart.windowEnd;
    chart.candles = mergeCandles(chart.candles, incomingCandles);
    chart.signals = mergeSignals(chart.signals, incomingSignals);
    chart.liquidations = mergeLiquidations(chart.liquidations, incomingLiquidations);
    chart.liquidationsTruncated = chart.liquidationsTruncated || !!view.liquidations_truncated;
    const firstIndex = oldFirst ? chart.candles.findIndex(item => item.time === oldFirst) : 0;
    chart.windowEnd = firstIndex + oldEnd;
  } else {
    const previousLast = chart.candles[chart.candles.length - 1]?.time;
    chart.candles = mergeCandles(chart.candles, incomingCandles);
    chart.signals = mergeSignals(chart.signals, incomingSignals);
    chart.liquidations = mergeLiquidations(chart.liquidations, incomingLiquidations);
    chart.liquidationsTruncated = chart.liquidationsTruncated || !!view.liquidations_truncated;
    const latestChanged = previousLast && chart.candles[chart.candles.length - 1]?.time !== previousLast;
    if (chart.atLatest) chart.windowEnd = chart.candles.length;
    else if (latestChanged) chart.newData = true;
  }
  if (older || reset) {
    chart.hasMore = !!view.has_more;
    chart.nextBefore = view.next_before || '';
  }
  cache.market = view;
  const realtimePrice = view.realtime_price;
  if (finite(realtimePrice?.trade_id) && realtimePrice.trade_id >= chart.latestPriceTradeID) {
    chart.latestPriceTradeID = realtimePrice.trade_id;
    chart.latestPriceTick = realtimePrice;
  }
  if (chart.latestPriceTick) applyPriceTick(chart.latestPriceTick, false, true);
  $('new-data').classList.toggle('hidden', !chart.newData);
}

function applyPriceTick(tick, render = true, force = false) {
  const tradeID = Number(tick?.trade_id);
  const tickTime = new Date(tick?.time).getTime();
  if (tick?.symbol !== symbol || !finite(tick?.price) || tick.price <= 0 || !Number.isFinite(tradeID) || !Number.isFinite(tickTime)) return;
  if (!force && tradeID <= chart.latestPriceTradeID) return;
  if (tradeID >= chart.latestPriceTradeID) {
    chart.latestPriceTradeID = tradeID;
    chart.latestPriceTick = tick;
  }
  if (!cache.market?.summary) return;
  const summary = cache.market.summary;
  const open24h = summary.last_price - summary.change_24h;
  summary.last_price = tick.price;
  summary.high_24h = Math.max(summary.high_24h || tick.price, tick.price);
  summary.low_24h = summary.low_24h > 0 ? Math.min(summary.low_24h, tick.price) : tick.price;
  summary.updated_at = tick.time;
  if (open24h > 0) {
    summary.change_24h = tick.price - open24h;
    summary.change_pct_24h = summary.change_24h / open24h * 100;
  }
  cache.market.realtime_price = tick;
  const duration = intervalMilliseconds[chart.interval] || 900000;
  const bucket = Math.floor(tickTime / duration) * duration;
  const last = chart.candles[chart.candles.length - 1];
  const lastTime = last ? new Date(last.time).getTime() : NaN;
  if (last && lastTime === bucket) {
    last.close = tick.price;
    last.high = Math.max(last.high, tick.price);
    last.low = Math.min(last.low, tick.price);
    last.complete = false;
    last.patterns = [];
  } else if (!last || lastTime < bucket) {
    chart.candles.push({ time: new Date(bucket).toISOString(), open: tick.price, high: tick.price, low: tick.price, close: tick.price, volume_usd: 0, exchange_count: 1, complete: false, patterns: [] });
    if (chart.atLatest) chart.windowEnd = chart.candles.length;
  }
  updateTicker(cache.market);
  if (!render) return;
  if (chart.atLatest) draw(cache.market, cache.map, cache.signal, cache.volume);
  else {
    chart.newData = true;
    $('new-data').classList.remove('hidden');
  }
}

function visibleCandles() {
  const end = Math.max(0, Math.min(chart.windowEnd, chart.candles.length));
  return chart.candles.slice(Math.max(0, end - chart.visibleCount), end);
}

function currentRange(candles, bins, signal, volume) {
  if (chart.yManual && finite(chart.yLow) && finite(chart.yHigh) && chart.yHigh > chart.yLow) return [chart.yLow, chart.yHigh];
  const values = [];
  candles.forEach(item => values.push(item.low, item.high));
  bins.forEach(item => values.push(item.price));
  (volume?.bins || []).forEach(item => values.push(item.price_low, item.price_high));
  [volume?.val, volume?.vah].forEach(value => { if (finite(value) && value > 0) values.push(value); });
  [signal?.mark_price].forEach(value => { if (finite(value) && value > 0) values.push(value); });
  if (!values.length) return [0, 1];
  let low = Math.min(...values), high = Math.max(...values);
  const padding = (high - low) * .025 || Math.max(high * .001, 1);
  low -= padding;
  high += padding;
  chart.yLow = low;
  chart.yHigh = high;
  chart.autoSpan = high - low;
  return [low, high];
}

function arrowPath(x, cy, side, size) {
  return side === 'long'
    ? 'M ' + x + ' ' + (cy - size) + ' L ' + (x - size) + ' ' + (cy + size) + ' L ' + (x + size) + ' ' + (cy + size) + ' Z'
    : 'M ' + x + ' ' + (cy + size) + ' L ' + (x - size) + ' ' + (cy - size) + ' L ' + (x + size) + ' ' + (cy - size) + ' Z';
}

function renderLiquidationTop3(map) {
  const container = $('liquidation-top3');
  container.replaceChildren();
  if (map?.data_source !== 'coinglass_binance_liqmap') {
    container.appendChild(Object.assign(document.createElement('span'), { textContent: 'CoinGlass 清算数据不可用' }));
    return;
  }
  [['long', '多头清算 Top3', map.top_long_liquidations || []], ['short', '空头清算 Top3', map.top_short_liquidations || []]].forEach(([className, label, peaks]) => {
    const span = document.createElement('span');
    span.className = className;
    span.textContent = label + '：' + (peaks.length ? peaks.map((peak, index) => (index + 1) + ') ' + money(peak.price)).join(' / ') : '—');
    span.title = peaks.map((peak, index) => (index + 1) + ') ' + money(peak.price) + ' · $' + compact(peak.amount_usd)).join('\n');
    container.appendChild(span);
  });
}

function layoutRankLabels(items, minimumY, maximumY, gap = 20) {
  const labels = items.slice().sort((a, b) => a.targetY - b.targetY);
  labels.forEach(item => { item.labelY = Math.max(minimumY, Math.min(maximumY, item.targetY)); });
  for (let index = 1; index < labels.length; index++) {
    labels[index].labelY = Math.max(labels[index].labelY, labels[index - 1].labelY + gap);
  }
  if (labels.length && labels[labels.length - 1].labelY > maximumY) {
    const overflow = labels[labels.length - 1].labelY - maximumY;
    labels.forEach(item => { item.labelY -= overflow; });
  }
  for (let index = labels.length - 2; index >= 0; index--) {
    labels[index].labelY = Math.min(labels[index].labelY, labels[index + 1].labelY - gap);
  }
  if (labels.length && labels[0].labelY < minimumY) {
    const underflow = minimumY - labels[0].labelY;
    labels.forEach(item => { item.labelY += underflow; });
  }
  return labels;
}

function drawLiquidationRankLabels(svg, map, low, high, y, top, plotBottom) {
  if (map?.data_source !== 'coinglass_binance_liqmap') return;
  const labels = [];
  [['short', '空', '#2dd4bf', map.top_short_liquidations || []], ['long', '多', '#fb7185', map.top_long_liquidations || []]].forEach(([side, prefix, color, peaks]) => {
    peaks.slice(0, 3).forEach((peak, index) => {
      if (!finite(peak.price) || peak.price < low || peak.price > high) return;
      labels.push({ side, prefix, color, rank: index + 1, price: peak.price, amount: peak.amount_usd, targetY: y(peak.price) });
    });
  });
  const positioned = layoutRankLabels(labels, top + 10, plotBottom - 10);
  positioned.forEach(item => {
    const text = item.prefix + item.rank + ' ' + money(item.price);
    const tip = (item.side === 'short' ? '空头清算' : '多头清算') + ' Top ' + item.rank + '\n价格 ' + money(item.price) + '\n清算强度 $' + compact(item.amount);
    const group = svgNode('g', { 'data-tip': tip });
    group.appendChild(svgNode('circle', { cx: dims.axisPriceRight + 5, cy: item.targetY, r: 2.2, fill: item.color }));
    group.appendChild(svgNode('path', { d: 'M ' + (dims.axisPriceRight + 5) + ' ' + item.targetY + ' L ' + (dims.rankLabelLeft - 8) + ' ' + item.targetY + ' L ' + (dims.rankLabelLeft - 2) + ' ' + item.labelY, fill: 'none', stroke: item.color, 'stroke-width': 1.15, opacity: .9 }));
    group.appendChild(svgNode('rect', { x: dims.rankLabelLeft, y: item.labelY - 9, width: dims.rankLabelRight - dims.rankLabelLeft, height: 18, fill: '#071019', stroke: item.color, 'stroke-width': 1, rx: 4 }));
    group.appendChild(svgNode('text', { x: dims.rankLabelLeft + 6, y: item.labelY + 3.5, fill: item.color, 'font-size': 10.5, 'font-weight': 750 }, text));
    svg.appendChild(group);
  });
}

function liquidationRadius(notionalUSD) {
  const amount = Math.max(100, finite(notionalUSD) ? notionalUSD : 0);
  return Math.max(3, Math.min(18, 3 + Math.log10(amount / 100) * 2.5));
}

function chartTimeScale(candles, left, right) {
  if (!candles.length) return null;
  const duration = intervalMilliseconds[chart.interval] || 900000;
  const lastStart = new Date(candles[candles.length - 1].time).getTime();
  if (!Number.isFinite(lastStart)) return null;
  const end = lastStart + duration;
  const start = end - chart.visibleCount * duration;
  const width = right - left;
  return { start, end, duration, step: width / chart.visibleCount, x: value => left + (value - start) / (end - start) * width };
}

function timeGridStep(span, width, minimumPixels, candidates) {
  return candidates.find(step => step * width / span >= minimumPixels) || candidates[candidates.length - 1];
}

function drawTimeGrid(svg, scale, top, plotBottom, labelY, left, right) {
  const fiveMinutes = 5 * 60 * 1000;
  const hour = 60 * 60 * 1000;
  const span = scale.end - scale.start;
  const width = right - left;
  const minorStep = timeGridStep(span, width, 4, [fiveMinutes, 2 * fiveMinutes, 3 * fiveMinutes, 6 * fiveMinutes, hour, 2 * hour, 4 * hour, 8 * hour, 12 * hour, 24 * hour, 48 * hour, 7 * 24 * hour]);
  const labelStep = timeGridStep(span, width, 90, [hour, 2 * hour, 3 * hour, 4 * hour, 6 * hour, 8 * hour, 12 * hour, 24 * hour, 48 * hour, 72 * hour, 7 * 24 * hour, 14 * 24 * hour, 30 * 24 * hour]);
  for (let at = Math.ceil(scale.start / minorStep) * minorStep; at < scale.end; at += minorStep) {
    if (at % labelStep === 0) continue;
    const x = scale.x(at);
    svg.appendChild(svgNode('line', { x1: x, y1: top, x2: x, y2: plotBottom, stroke: '#132a36', 'stroke-width': .7, opacity: .58 }));
  }
  for (let at = Math.ceil(scale.start / labelStep) * labelStep; at < scale.end; at += labelStep) {
    const x = scale.x(at);
    const label = new Date(at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
    svg.appendChild(svgNode('line', { x1: x, y1: top, x2: x, y2: plotBottom, stroke: '#315064', 'stroke-width': 1, opacity: .92 }));
    svg.appendChild(svgNode('text', { x, y: labelY, 'text-anchor': 'middle', fill: '#7895a7', 'font-size': 11 }, label));
  }
}

function drawLiquidationBubbles(plot, candles, events, y, xForTime, left, candleRight, top, plotBottom) {
  if (!candles.length || !events.length) return;
  const duration = intervalMilliseconds[chart.interval] || 900000;
  const starts = new Map(candles.map((candle, index) => [new Date(candle.time).getTime(), index]));
  const placed = [];
  events.forEach(event => {
    const eventTime = new Date(event.event_time).getTime();
    if (!Number.isFinite(eventTime) || !finite(event.price) || (event.position_side !== 'long' && event.position_side !== 'short')) return;
    const bucket = Math.floor(eventTime / duration) * duration;
    const candleIndex = starts.get(bucket);
    if (candleIndex === undefined) return;
    const baseX = xForTime(eventTime);
    if (baseX < left || baseX > candleRight) return;
    let x = baseX;
    const radius = liquidationRadius(event.notional_usd);
    const long = event.position_side === 'long';
    const displayPrice = event.price + (long ? -liquidationPriceOffsetUSD : liquidationPriceOffsetUSD);
    const cy = y(displayPrice);
    if (!finite(cy) || cy < top || cy > plotBottom) return;
    for (let attempt = 0; attempt < 8; attempt++) {
      const overlap = placed.find(item => Math.hypot(x - item.x, cy - item.y) < radius + item.radius + 2);
      if (!overlap) break;
      const shift = attempt + 1;
      x = baseX + (shift % 2 ? 1 : -1) * Math.ceil(shift / 2) * 4;
    }
    placed.push({ x, y: cy, radius });
    const color = long ? '#fb7185' : '#2dd4bf';
    const tip = 'Binance ' + (long ? '多单爆仓' : '空单爆仓') + '\n时间 ' + when(event.event_time) + '\n爆仓价格 ' + money(event.price) + '\n圆圈显示价 ' + money(displayPrice) + '（' + (long ? '-' : '+') + liquidationPriceOffsetUSD + ' USDT）\n数量 ' + Intl.NumberFormat('en-US', { maximumFractionDigits: 8 }).format(event.quantity || 0) + '\n爆仓金额 $' + compact(event.notional_usd) + '\n覆盖：强平流每交易对每秒最近一笔';
    plot.appendChild(svgNode('circle', { cx: x, cy, r: radius, fill: color, 'fill-opacity': .38, stroke: color, 'stroke-width': 1.5, 'data-tip': tip }));
  });
}

function drawLiquidationBiasArrow(svg, map, currentPrice, y, low, high) {
  if (map?.data_source !== 'coinglass_binance_liqmap' || !finite(currentPrice) || currentPrice < low || currentPrice > high) return;
  const aboveUSD = Number(map.liquidation_above_usd);
  const belowUSD = Number(map.liquidation_below_usd);
  if (!Number.isFinite(aboveUSD) || !Number.isFinite(belowUSD) || map.liquidation_direction === 'balanced') return;
  const upward = map.liquidation_direction === 'up';
  if (!upward && map.liquidation_direction !== 'down') return;
  const color = upward ? '#22c55e' : '#ef4444';
  const currentY = y(currentPrice);
  const x = dims.liquidationLeft + 25;
  const tipY = currentY + (upward ? -28 : 28);
  const shaftEndY = currentY + (upward ? -17 : 17);
  const shaftStartY = currentY + (upward ? 13 : -13);
  const tip = 'CoinGlass 10分钟快照清算方向\n抓取时现价 ' + money(map.mark_price) + '\n现价上方合计 $' + compact(aboveUSD) + '\n现价下方合计 $' + compact(belowUSD) + '\n判断：' + (upward ? '下方金额更大，向上清算' : '上方金额更大，向下清算') + '\n数据时间 ' + when(map.captured_at);
  const group = svgNode('g', { 'data-tip': tip });
  group.appendChild(svgNode('line', { x1: x, y1: shaftStartY, x2: x, y2: shaftEndY, stroke: color, 'stroke-width': 6, 'stroke-linecap': 'round' }));
  group.appendChild(svgNode('path', { d: upward ? 'M ' + x + ' ' + tipY + ' L ' + (x - 10) + ' ' + (tipY + 13) + ' L ' + (x + 10) + ' ' + (tipY + 13) + ' Z' : 'M ' + x + ' ' + tipY + ' L ' + (x - 10) + ' ' + (tipY - 13) + ' L ' + (x + 10) + ' ' + (tipY - 13) + ' Z', fill: color, stroke: '#07141d', 'stroke-width': 1.2 }));
  svg.appendChild(group);
}

function draw(view, map, signal, volume) {
  const svg = $('market-chart');
  svg.replaceChildren();
  const candles = visibleCandles();
  const bins = map?.bins || [];
  $('chart-title').textContent = (intervalName[chart.interval] || chart.interval) + ' K线 · 成交量分布 · 清算墙';
  const sessionRange = volume?.session_start ? ' (' + when(volume.session_start) + ' → ' + when(volume.session_end) + ')' : '';
  const profileState = volume?.state === 'ok' ? '当前时段成交量' + sessionRange : volume?.state === 'backfilling' ? '成交量回填 ' + ((volume.backfill_progress || 0) * 100).toFixed(0) + '%' + sessionRange : volume?.state === 'stale' ? '成交量已过期' + sessionRange : '成交量不可用';
  const leverageText = (map?.leverages || []).map(value => value + '×').join('/');
  const coinGlassMap = map?.data_source === 'coinglass_binance_liqmap';
  const liquidationName = coinGlassMap ? 'CoinGlass Binance 清算墙' : 'CoinGlass 清算墙不可用';
  renderLiquidationTop3(map);
  const liquidationEventState = ' · Binance爆仓≥$' + compact(chart.liquidationMinimumUSD) + ' ' + chart.liquidations.length + '笔' + (chart.liquidationsTruncated ? '（仅最近5000笔）' : '');
  $('chart-context').textContent = (chart.atLatest ? '最新K线' : '历史K线') + ' · ' + profileState + ' / ' + liquidationName + ' · 共用价格纵轴' + liquidationEventState + (leverageText ? ' · 杠杆 ' + leverageText : '') + (coinGlassMap && map.captured_at ? ' · 抓取 ' + when(map.captured_at) : '');
  if (!candles.length && !bins.length && !(volume?.bins || []).length) {
    svg.appendChild(svgNode('text', { x: 800, y: 280, 'text-anchor': 'middle', fill: '#83a0b2' }, '等待 K 线、成交量分布与清算地图数据'));
    return;
  }
  const { height, left, top, bottom, candleRight, volumeLeft, volumeRight, liquidationLeft, profileRight, axisPriceRight } = dims;
  const [low, high] = currentRange(candles, bins, signal, volume);
  const plotBottom = height - bottom;
  const y = price => top + (high - price) / (high - low) * (plotBottom - top);
  const defs = svgNode('defs');
  const clip = svgNode('clipPath', { id: 'plot-clip' });
  clip.appendChild(svgNode('rect', { x: left, y: top, width: profileRight - left, height: plotBottom - top }));
  defs.appendChild(clip);
  svg.appendChild(defs);
  for (let i = 0; i < 6; i++) {
    const price = high - (high - low) * i / 5;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: '#18313f', 'stroke-width': 1 }));
    svg.appendChild(svgNode('text', { x: left - 8, y: yy + 4, 'text-anchor': 'end', fill: '#7895a7', 'font-size': 11 }, money(price)));
    svg.appendChild(svgNode('line', { x1: profileRight, y1: yy, x2: profileRight + 6, y2: yy, stroke: '#527083', 'stroke-width': 1 }));
    svg.appendChild(svgNode('text', { x: axisPriceRight, y: yy + 4, 'text-anchor': 'end', fill: '#9ab4c4', 'font-size': 11 }, money(price)));
  }
  svg.appendChild(svgNode('line', { x1: volumeLeft - 18, y1: top, x2: volumeLeft - 18, y2: plotBottom, stroke: '#315064', 'stroke-width': 1 }));
  svg.appendChild(svgNode('line', { x1: liquidationLeft - 18, y1: top, x2: liquidationLeft - 18, y2: plotBottom, stroke: '#315064', 'stroke-width': 1 }));
  svg.appendChild(svgNode('line', { x1: profileRight, y1: top, x2: profileRight, y2: plotBottom, stroke: '#527083', 'stroke-width': 1 }));
  const timeScale = chartTimeScale(candles, left, candleRight);
  if (timeScale) drawTimeGrid(svg, timeScale, top, plotBottom, height - 17, left, candleRight);
  const plot = svgNode('g', { 'clip-path': 'url(#plot-clip)' });
  if (candles.length) {
    const bodyWidth = Math.max(2, timeScale.step * .58);
    const modelSignals = new Map();
    chart.signals.forEach(item => {
      const key = new Date(item.candle_time).getTime();
      if (!modelSignals.has(key)) modelSignals.set(key, []);
      modelSignals.get(key).push(item);
    });
    drawLiquidationBubbles(plot, candles, chart.liquidations, y, timeScale.x, left, candleRight, top, plotBottom);
    candles.forEach(candle => {
      const candleStart = new Date(candle.time).getTime();
      const x = timeScale.x(candleStart + timeScale.duration / 2);
      const color = candle.close >= candle.open ? '#2dd4bf' : '#fb7185';
      const bodyTop = y(Math.max(candle.open, candle.close));
      const bodyBottom = y(Math.min(candle.open, candle.close));
      const patterns = (candle.patterns || []).map(item => patternName[item.name] || item.name).join(' · ') || '无识别形态';
      const group = svgNode('g', { 'data-tip': when(candle.time) + '\nO ' + money(candle.open) + '  H ' + money(candle.high) + '\nL ' + money(candle.low) + '  C ' + money(candle.close) + '\n成交额 $' + compact(candle.volume_usd) + '\n' + patterns + (candle.complete ? '' : '\n当前K线尚未完成') });
      group.appendChild(svgNode('line', { x1: x, y1: y(candle.high), x2: x, y2: y(candle.low), stroke: color, 'stroke-width': 1.15 }));
      group.appendChild(svgNode('rect', { x: x - bodyWidth / 2, y: bodyTop, width: bodyWidth, height: Math.max(1.5, bodyBottom - bodyTop), fill: color, rx: .8 }));
      const bullish = (candle.patterns || []).filter(item => item.bias === 'bullish');
      const bearish = (candle.patterns || []).filter(item => item.bias === 'bearish');
      if (bullish.length) {
        const cy = y(candle.low) + 9;
        group.appendChild(svgNode('path', { d: arrowPath(x, cy, 'long', 4), fill: 'none', stroke: '#2dd4bf', 'stroke-width': 1.4, 'data-tip': 'K线形态 · 多单方向\n' + bullish.map(item => patternName[item.name] || item.name).join(' · ') + '\n' + when(candle.time) }));
      }
      if (bearish.length) {
        const cy = y(candle.high) - 9;
        group.appendChild(svgNode('path', { d: arrowPath(x, cy, 'short', 4), fill: 'none', stroke: '#fb7185', 'stroke-width': 1.4, 'data-tip': 'K线形态 · 空单方向\n' + bearish.map(item => patternName[item.name] || item.name).join(' · ') + '\n' + when(candle.time) }));
      }
      const signals = modelSignals.get(new Date(candle.time).getTime()) || [];
      signals.forEach(item => {
        const patterned = item.side === 'long' ? bullish.length : bearish.length;
        const cy = item.side === 'long' ? y(candle.low) + 10 + (patterned ? 11 : 0) : y(candle.high) - 10 - (patterned ? 11 : 0);
        const signalColor = item.side === 'long' ? '#2dd4bf' : '#fb7185';
        group.appendChild(svgNode('path', { d: arrowPath(x, cy, item.side, 5), fill: signalColor, stroke: '#07141d', 'stroke-width': 1, 'data-tip': '模型' + (item.side === 'long' ? '多单' : '空单') + '信号 · ' + pct(item.probability) + '\n预测价 ' + money(item.price) + '\n预测时间 ' + when(item.time) + '\n模型 ' + (item.model_version || '—') + '\n仅表示60分钟先触墙概率' }));
      });
      plot.appendChild(group);
    });
  }
  if (bins.length) {
    const maximum = Math.max(...bins.map(item => item.total_usd), 1);
    const binWidth = map?.bin_width || (high - low) / Math.max(1, bins.length);
    const barHeight = Math.max(2, Math.abs(y(low + binWidth) - y(low)) * .9);
    const leverageColors = { '10': '#7dd3fc', '25': '#6188ff', '50': '#f5c400', '100': '#f08833' };
    const topRanks = new Map();
    (map.top_long_liquidations || []).forEach((peak, index) => topRanks.set(String(peak.price), { label: '多' + (index + 1), color: '#fb7185' }));
    (map.top_short_liquidations || []).forEach((peak, index) => topRanks.set(String(peak.price), { label: '空' + (index + 1), color: '#2dd4bf' }));
    bins.forEach(bin => {
      if (bin.price < low - binWidth || bin.price > high + binWidth) return;
      const width = Math.sqrt(bin.total_usd / maximum) * (profileRight - liquidationLeft);
      const color = bin.price < (map.mark_price || signal?.mark_price) ? '#fb7185' : '#2dd4bf';
      const leverageRows = Object.entries(bin.leverage_usd || {}).filter(([, amount]) => amount > 0).sort((a, b) => Number(a[0]) - Number(b[0]));
      const leverageTip = leverageRows.map(([leverage, amount]) => '\n' + leverage + 'x $' + compact(amount)).join('');
      const tip = liquidationName + '\n价格 ' + money(bin.price) + '\n总清算强度 $' + compact(bin.total_usd) + leverageTip + '\n多仓 $' + compact(bin.long_usd) + '\n空仓 $' + compact(bin.short_usd);
      if (leverageRows.length) {
        let cursor = liquidationLeft;
        leverageRows.forEach(([leverage, amount]) => {
          const segmentWidth = width * amount / bin.total_usd;
          plot.appendChild(svgNode('rect', { x: cursor, y: y(bin.price) - barHeight / 2, width: segmentWidth, height: barHeight, fill: leverageColors[leverage] || color, opacity: .4 + .58 * bin.total_usd / maximum, 'data-tip': tip }));
          cursor += segmentWidth;
        });
      } else {
        plot.appendChild(svgNode('rect', { x: liquidationLeft, y: y(bin.price) - barHeight / 2, width, height: barHeight, fill: color, opacity: .35 + .6 * bin.total_usd / maximum, 'data-tip': tip }));
      }
      const rank = topRanks.get(String(bin.price));
      if (rank) {
        plot.appendChild(svgNode('rect', { x: liquidationLeft, y: y(bin.price) - Math.max(3, barHeight / 2 + 1), width: Math.max(width, 25), height: Math.max(6, barHeight + 2), fill: 'none', stroke: rank.color, 'stroke-width': 1.4, opacity: .98, 'data-tip': rank.label + ' · ' + tip }));
        plot.appendChild(svgNode('text', { x: liquidationLeft + 3, y: y(bin.price) + 3, fill: '#f8fafc', 'font-size': 9, 'font-weight': 750, 'data-tip': rank.label + ' · ' + tip }, rank.label));
      }
    });
  }
  const volumeBins = volume?.bins || [];
  if (volumeBins.length) {
    const maximum = Math.max(...volumeBins.map(item => item.volume_usd), 1);
    volumeBins.forEach(bin => {
      const center = (bin.price_low + bin.price_high) / 2;
      if (bin.price_high < low || bin.price_low > high) return;
      const width = Math.sqrt(bin.volume_usd / maximum) * (volumeRight - volumeLeft);
      const barHeight = Math.max(2, Math.abs(y(bin.price_low) - y(bin.price_high)) * .9);
      const topThree = bin.volume_rank >= 1 && bin.volume_rank <= 3;
      const fill = topThree ? '#a855f7' : '#60a5fa';
      const opacity = topThree ? .96 : bin.in_value_area ? .88 : .28;
      const rankTip = topThree ? '\n成交额排名 #' + bin.volume_rank : '';
      plot.appendChild(svgNode('rect', { x: volumeRight - width, y: y(center) - barHeight / 2, width, height: barHeight, fill, opacity, 'data-tip': 'Binance 当前时段成交量\n价格 ' + money(bin.price_low) + ' – ' + money(bin.price_high) + '\n成交额 $' + compact(bin.volume_usd) + '\n占比 ' + (bin.volume_percent || 0).toFixed(2) + '%' + rankTip + (bin.in_value_area ? '\n70%价值区域内' : '\n价值区域外') }));
    });
  }
  svg.appendChild(plot);
  svg.appendChild(svgNode('text', { x: (volumeLeft + volumeRight) / 2, y: top + 12, 'text-anchor': 'middle', fill: '#7dd3fc', 'font-size': 11, 'font-weight': 650 }, '当前时段成交量'));
  svg.appendChild(svgNode('text', { x: (liquidationLeft + profileRight) / 2, y: top + 12, 'text-anchor': 'middle', fill: coinGlassMap ? '#7dd3fc' : '#83a0b2', 'font-size': 11, 'font-weight': 650 }, liquidationName));
  const currentPrice = view?.summary?.last_price || signal?.mark_price;
  const levels = [['现价', currentPrice, '#f8fafc', '4 5']];
  levels.forEach(([name, price, color, dash]) => {
    if (!price || price < low || price > high) return;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: color, 'stroke-width': name === '现价' ? 1.2 : 1.8, 'stroke-dasharray': dash, opacity: .95 }));
    svg.appendChild(svgNode('rect', { x: profileRight - 112, y: yy - 12, width: 112, height: 22, fill: '#071019', stroke: color, rx: 5 }));
    svg.appendChild(svgNode('text', { x: profileRight - 6, y: yy + 4, 'text-anchor': 'end', fill: color, 'font-size': 11 }, name + ' ' + money(price)));
  });
  [['VAL', volume?.val, '#22c55e'], ['VAH', volume?.vah, '#ef4444']].forEach(([name, price, color]) => {
    if (!finite(price) || price < low || price > high) return;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: color, 'stroke-width': 1.7, opacity: .95 }));
    svg.appendChild(svgNode('rect', { x: volumeRight - 112, y: yy - 12, width: 112, height: 22, fill: '#071019', stroke: color, rx: 5 }));
    svg.appendChild(svgNode('text', { x: volumeRight - 6, y: yy + 4, 'text-anchor': 'end', fill: color, 'font-size': 11 }, name + ' ' + money(price)));
  });
  drawLiquidationBiasArrow(svg, map, currentPrice, y, low, high);
  drawLiquidationRankLabels(svg, map, low, high, y, top, plotBottom);
  bindTips(svg);
}

function bindTips(svg) {
  const tooltip = $('tooltip');
  svg.querySelectorAll('[data-tip]').forEach(element => {
    element.addEventListener('pointermove', event => {
      event.stopPropagation();
      tooltip.textContent = element.getAttribute('data-tip');
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(innerWidth - 230, event.clientX + 14) + 'px';
      tooltip.style.top = Math.min(innerHeight - 120, event.clientY + 14) + 'px';
    });
    element.addEventListener('pointerleave', () => { tooltip.style.display = 'none'; });
  });
}

function showFailures(failures) {
  const all = [...failures];
  if (streamError) all.push('实时推送 · ' + streamError);
  $('api-errors').textContent = all.length ? '数据接口异常：\n' + all.join('\n') : '';
  $('api-errors').classList.toggle('hidden', all.length === 0);
}

async function loadOlder() {
  if (chart.loadingOlder || !chart.hasMore || !chart.nextBefore) return;
  chart.loadingOlder = true;
  const requestedSymbol = symbol;
  const requestedInterval = chart.interval;
  try {
    const view = await json(marketPath(chart.nextBefore));
    if (requestedSymbol !== symbol || requestedInterval !== chart.interval) return;
    mergeMarket(view, true, false);
    draw(cache.market, cache.map, cache.signal, cache.volume);
  } catch (error) {
    lastFailures = lastFailures.filter(item => !item.startsWith('历史行情'));
    lastFailures.push('历史行情 · ' + error.message);
    showFailures(lastFailures);
  } finally {
    chart.loadingOlder = false;
  }
}

async function refresh(resetMarket = false) {
  const sequence = ++refreshSequence;
  const requestedSymbol = symbol;
  const requestedInterval = chart.interval;
  refreshing = true;
  try {
    const specs = [['signal', '预测', 'signals/latest?symbol=' + symbol], ['map', '清算地图', 'map?symbol=' + symbol], ['volume', '成交量分布', 'volume-profile?symbol=' + symbol], ['market', '市场行情', marketPath()], ['schedule', '抓取计划', 'coinglass/schedule']];
    const results = await Promise.allSettled(specs.map(item => json(item[2])));
    if (requestedSymbol !== symbol || requestedInterval !== chart.interval || sequence !== refreshSequence) return;
    lastFailures = [];
    results.forEach((result, index) => {
      const [key, label] = specs[index];
      if (result.status === 'fulfilled') {
        if (key === 'market') mergeMarket(result.value, false, resetMarket || !chart.candles.length);
        else if (key === 'schedule') cache.schedule = { ...result.value, received_at_ms: Date.now() };
        else cache[key] = result.value;
      } else {
        if (key === 'map') cache.map = null;
        lastFailures.push(label + ' · ' + result.reason.message);
      }
    });
    if (cache.market) updateTicker(cache.market);
    if (cache.signal) {
      updateProbabilities(cache.signal.probabilities);
      updateTrigger('upper', cache.signal.upper_trigger);
      updateTrigger('lower', cache.signal.lower_trigger);
      $('reason').textContent = cache.signal.state === 'ok' ? '当前领先类别：' + (cache.signal.leading_class || '—') + '；触发顺序：' + (cache.signal.trigger_order || 'pending') + '。' : (cache.signal.reason || '清算墙或历史数据不足');
      $('model-info').textContent = '模型：' + (cache.signal.model_version || '尚未训练') + ' · 数据年龄 ' + (finite(cache.signal.data_age_seconds) ? Math.max(0, cache.signal.data_age_seconds).toFixed(0) + ' 秒' : '—');
    }
    draw(cache.market, cache.map, cache.signal, cache.volume);
    updateCoinGlassCountdown();
    showFailures(lastFailures);
  } finally {
    if (sequence === refreshSequence) refreshing = false;
  }
}

function svgPoint(event) {
  const rect = $('market-chart').getBoundingClientRect();
  return { x: (event.clientX - rect.left) * dims.width / rect.width, y: (event.clientY - rect.top) * dims.height / rect.height };
}

function installChartInteractions() {
  const svg = $('market-chart');
  const wrap = svg.parentElement;
  svg.addEventListener('wheel', event => {
    event.preventDefault();
    if (!finite(chart.yLow) || !finite(chart.yHigh)) return;
    const point = svgPoint(event);
    const plotHeight = dims.height - dims.top - dims.bottom;
    const anchor = chart.yHigh - (point.y - dims.top) / plotHeight * (chart.yHigh - chart.yLow);
    const factor = event.deltaY > 0 ? 1.12 : 1 / 1.12;
    const current = cache.market?.summary?.last_price || cache.signal?.mark_price || 1;
    const atr = cache.map?.atr || cache.signal?.atr || 0;
    const minSpan = Math.max(atr * .25, current * .001, 1e-8);
    const maxSpan = Math.max((chart.autoSpan || chart.yHigh - chart.yLow) * 8, minSpan);
    const oldSpan = chart.yHigh - chart.yLow;
    const span = Math.min(maxSpan, Math.max(minSpan, oldSpan * factor));
    const ratio = (anchor - chart.yLow) / oldSpan;
    chart.yLow = anchor - span * ratio;
    chart.yHigh = chart.yLow + span;
    chart.yManual = true;
    draw(cache.market, cache.map, cache.signal, cache.volume);
  }, { passive: false });
  svg.addEventListener('pointerdown', event => {
    const point = svgPoint(event);
    let mode = '';
    if (point.x >= dims.left && point.x <= dims.candleRight) mode = 'chart';
    else if (point.x >= dims.volumeLeft - 18 && point.x <= dims.profileRight) mode = 'price';
    if (!mode || !finite(chart.yLow) || !finite(chart.yHigh) || chart.yHigh <= chart.yLow) return;
    event.preventDefault();
    chart.drag = { mode, pointerId: event.pointerId, x: point.x, y: point.y, end: chart.windowEnd, low: chart.yLow, high: chart.yHigh };
    svg.setPointerCapture(event.pointerId);
    wrap.classList.add('dragging');
  });
  svg.addEventListener('pointermove', event => {
    if (!chart.drag || chart.drag.pointerId !== event.pointerId) return;
    const point = svgPoint(event);
    if (chart.drag.mode === 'chart') {
      const step = (dims.candleRight - dims.left) / chart.visibleCount;
      const shift = Math.round((chart.drag.x - point.x) / step);
      const minimum = Math.min(chart.visibleCount, chart.candles.length);
      chart.windowEnd = Math.max(minimum, Math.min(chart.candles.length, chart.drag.end + shift));
      chart.atLatest = chart.windowEnd === chart.candles.length;
      if (!chart.atLatest) chart.newData = false;
      $('new-data').classList.toggle('hidden', !chart.newData);
    }
    if (chart.drag.mode === 'chart' || chart.drag.mode === 'price') {
      const span = chart.drag.high - chart.drag.low;
      const delta = (point.y - chart.drag.y) / (dims.height - dims.top - dims.bottom) * span;
      chart.yLow = chart.drag.low + delta;
      chart.yHigh = chart.drag.high + delta;
      chart.yManual = true;
    }
    draw(cache.market, cache.map, cache.signal, cache.volume);
  });
  const endDrag = event => {
    if (!chart.drag || chart.drag.pointerId !== event.pointerId) return;
    if (chart.drag.mode === 'chart' && chart.windowEnd <= chart.visibleCount + 12) loadOlder();
    chart.drag = null;
    wrap.classList.remove('dragging');
  };
  svg.addEventListener('pointerup', endDrag);
  svg.addEventListener('pointercancel', endDrag);
}

function resetChartState() {
  chart.candles = [];
  chart.signals = [];
  chart.liquidations = [];
  chart.liquidationsTruncated = false;
  chart.liquidationMinimumUSD = defaultLiquidationMinimumUSD;
  chart.latestPriceTradeID = 0;
  chart.latestPriceTick = null;
  chart.windowEnd = 0;
  chart.hasMore = false;
  chart.nextBefore = '';
  chart.yLow = chart.yHigh = chart.autoSpan = null;
  chart.yManual = false;
  chart.atLatest = true;
  chart.newData = false;
  $('new-data').classList.add('hidden');
}

function formatLog(entry) {
  const fields = entry.fields && Object.keys(entry.fields).length ? ' ' + JSON.stringify(entry.fields) : '';
  return when(entry.time) + ' [' + (entry.level || 'INFO') + '] ' + (entry.message || '') + fields;
}

function renderLogs() {
  const list = $('log-list');
  list.replaceChildren();
  const fragment = document.createDocumentFragment();
  logEntries.forEach(entry => {
    const row = document.createElement('div');
    row.className = 'log-entry';
    const content = document.createElement('span');
    content.className = entry.level || 'INFO';
    content.textContent = formatLog(entry);
    row.appendChild(content);
    fragment.appendChild(row);
  });
  list.appendChild(fragment);
  $('log-more').disabled = !logCursor;
  $('log-more').textContent = logCursor ? '加载更早日志' : '没有更早日志';
}

async function loadLogs(older = false) {
  $('log-error').classList.add('hidden');
  const params = new URLSearchParams({ limit: '200', level: $('log-level').value });
  if (older && logCursor) params.set('before', logCursor);
  try {
    const page = await json('logs?' + params.toString());
    logEntries = older ? logEntries.concat(page.entries || []) : (page.entries || []);
    logCursor = page.next_before || '';
    renderLogs();
  } catch (error) {
    $('log-error').textContent = '日志读取失败：' + error.message;
    $('log-error').classList.remove('hidden');
  }
}

function openLogs() { $('log-drawer').classList.remove('hidden'); $('drawer-backdrop').classList.remove('hidden'); loadLogs(false); }
function closeLogs() { $('log-drawer').classList.add('hidden'); $('drawer-backdrop').classList.add('hidden'); }
function coinGlassBrowserURL() {
  const base = new URL('.', document.baseURI);
  const socketPath = base.pathname.replace(/^\/+/, '') + 'coinglass-login/websockify';
  const target = new URL('coinglass-login/vnc.html', base);
  target.searchParams.set('autoconnect', 'true');
  target.searchParams.set('resize', 'scale');
  target.searchParams.set('path', socketPath);
  return target.toString();
}
function openCoinGlass() {
  const target = coinGlassBrowserURL();
  $('browser-frame').src = target;
  $('browser-new-window').href = target;
  $('browser-backdrop').classList.remove('hidden');
  document.body.style.overflow = 'hidden';
}
function closeCoinGlass() {
  $('browser-backdrop').classList.add('hidden');
  $('browser-frame').removeAttribute('src');
  document.body.style.overflow = '';
}
async function captureCoinGlass() {
  const button = $('coinglass-capture');
  const state = $('coinglass-capture-state');
  button.disabled = true;
  captureStateHoldUntil = Number.POSITIVE_INFINITY;
  state.classList.remove('error');
  state.textContent = '正在刷新清算地图并读取响应…';
  try {
    const response = await fetch(api('coinglass/capture'), { method: 'POST', cache: 'no-store', credentials: 'same-origin' });
    let body = null;
    try { body = await response.json(); } catch (_) { /* handled below */ }
    if (response.status === 401) {
      location.reload();
      return;
    }
    if (!response.ok) throw new Error(body?.detail || ('HTTP ' + response.status));
    const bytes = (body.parsed || []).reduce((sum, item) => sum + (item.bytes || 0), 0);
    state.textContent = '抓取成功：' + (body.parsed || []).length + ' 组结构化 JSON，' + compact(bytes) + 'B · ' + when(body.captured_at);
    captureStateHoldUntil = Date.now() + 5000;
  } catch (error) {
    state.classList.add('error');
    state.textContent = '抓取失败：' + error.message;
    captureStateHoldUntil = Date.now() + 8000;
  } finally {
    button.disabled = false;
  }
}

document.querySelectorAll('button[data-symbol]').forEach(button => {
  button.addEventListener('click', () => {
    document.querySelectorAll('button[data-symbol]').forEach(item => item.classList.remove('active'));
    button.classList.add('active');
    symbol = button.dataset.symbol;
    cache.signal = cache.map = cache.market = cache.volume = null;
    resetChartState();
    refresh(true);
  });
});
document.querySelectorAll('button[data-interval]').forEach(button => {
  button.classList.toggle('active', button.dataset.interval === chart.interval);
  button.addEventListener('click', () => {
    if (chart.interval === button.dataset.interval) return;
    chart.interval = button.dataset.interval;
    try { localStorage.setItem('liquidation.interval', chart.interval); } catch (_) { /* keep current interval */ }
    document.querySelectorAll('button[data-interval]').forEach(item => item.classList.toggle('active', item === button));
    resetChartState();
    refresh(true);
  });
});
$('chart-latest').addEventListener('click', () => {
  chart.atLatest = true;
  chart.newData = false;
  $('new-data').classList.add('hidden');
  refresh(true);
});
$('chart-reset').addEventListener('click', () => {
  chart.yManual = false;
  chart.yLow = chart.yHigh = chart.autoSpan = null;
  draw(cache.market, cache.map, cache.signal, cache.volume);
});
$('log-button').addEventListener('click', openLogs);
$('coinglass-button').addEventListener('click', openCoinGlass);
$('coinglass-capture').addEventListener('click', captureCoinGlass);
$('coinglass-json').addEventListener('click', () => window.open(api('coinglass/latest'), '_blank', 'noopener'));
$('browser-close').addEventListener('click', closeCoinGlass);
$('browser-backdrop').addEventListener('click', event => { if (event.target === $('browser-backdrop')) closeCoinGlass(); });
document.addEventListener('keydown', event => { if (event.key === 'Escape' && !$('browser-backdrop').classList.contains('hidden')) closeCoinGlass(); });
$('log-close').addEventListener('click', closeLogs);
$('drawer-backdrop').addEventListener('click', closeLogs);
$('log-refresh').addEventListener('click', () => loadLogs(false));
$('log-more').addEventListener('click', () => loadLogs(true));
$('log-level').addEventListener('change', () => { logCursor = ''; loadLogs(false); });
$('log-copy').addEventListener('click', async () => {
  try { await navigator.clipboard.writeText(logEntries.map(formatLog).join('\n')); }
  catch (_) { $('log-error').textContent = '浏览器不允许复制日志'; $('log-error').classList.remove('hidden'); }
});

installChartInteractions();
refresh(true);
setInterval(() => refresh(false), 10000);
setInterval(updateCoinGlassCountdown, 1000);
setInterval(() => { if (!$('log-drawer').classList.contains('hidden') && $('log-auto').checked) loadLogs(false); }, 5000);

const events = new EventSource(api('stream'));
events.addEventListener('health', () => { streamError = ''; showFailures(lastFailures); });
events.addEventListener('prediction', event => {
  streamError = '';
  const prediction = JSON.parse(event.data);
  if (prediction.symbol === symbol) refresh(false);
});
events.addEventListener('price', event => {
  streamError = '';
  applyPriceTick(JSON.parse(event.data));
  showFailures(lastFailures);
});
events.addEventListener('liquidation', event => {
  streamError = '';
  const liquidation = JSON.parse(event.data);
  if (liquidation.symbol !== symbol || liquidation.exchange !== 'binance' || !finite(liquidation.notional_usd) || liquidation.notional_usd < chart.liquidationMinimumUSD) return;
  chart.liquidations = mergeLiquidations(chart.liquidations, [liquidation]);
  if (chart.atLatest) draw(cache.market, cache.map, cache.signal, cache.volume);
  else {
    chart.newData = true;
    $('new-data').classList.remove('hidden');
  }
  showFailures(lastFailures);
});
events.onerror = () => { streamError = '连接中断，正在重试'; showFailures(lastFailures); };
window.addEventListener('themechange', () => draw(cache.market, cache.map, cache.signal, cache.volume));
