'use strict';

const NS = 'http://www.w3.org/2000/svg';
const $ = id => document.getElementById(id);
const cache = { signal: null, map: null, market: null, volume: null };
const chart = {
  interval: localStorage.getItem('liquidation.interval') || '15m',
  candles: [], signals: [], windowEnd: 0, visibleCount: 120,
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

const api = path => new URL('api/v1/' + path, document.baseURI).toString();
const finite = value => typeof value === 'number' && Number.isFinite(value);
const money = value => finite(value) ? Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value) : '—';
const compact = value => finite(value) ? Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 2 }).format(value) : '—';
const pct = value => finite(value) ? (value * 100).toFixed(1) + '%' : '—';
const when = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const patternName = { doji: '十字星', hammer: '锤头', shooting_star: '流星', bullish_engulfing: '看涨吞没', bearish_engulfing: '看跌吞没' };
const statusName = { unavailable: '不可用', armed: '等待触发', approaching: '接近触发', triggered: '已触发', expired: '已过期' };
const intervalName = { '1m': '1分钟', '2m': '2分钟', '3m': '3分钟', '5m': '5分钟', '10m': '10分钟', '15m': '15分钟', '30m': '30分钟', '1h': '1小时', '4h': '4小时', '8h': '8小时', '12h': '12小时', '24h': '24小时' };
const dims = { height: 560, width: 1600, left: 72, top: 24, bottom: 42, candleRight: 1025, volumeLeft: 1060, volumeRight: 1280, liquidationLeft: 1315, profileRight: 1565 };
if (!intervalName[chart.interval]) chart.interval = '15m';

function svgNode(tag, attrs = {}, text = '') {
  const element = document.createElementNS(NS, tag);
  Object.entries(attrs).forEach(([key, value]) => element.setAttribute(key, value));
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

function updateTicker(view) {
  const summary = view?.summary;
  if (!summary) return;
  $('ticker-symbol').textContent = (view.symbol || symbol) + ' · Binance 永续价格';
  $('ticker-price').textContent = money(summary.last_price);
  $('ticker-updated').textContent = '更新 ' + when(summary.updated_at) + (view.backfill_complete ? '' : ' · 180天历史回填中');
  const change = finite(summary.change_pct_24h) ? summary.change_pct_24h : 0;
  const up = change >= 0;
  $('ticker-change').textContent = (up ? '+' : '') + change.toFixed(2) + '%';
  $('ticker-change').className = 'value ' + (up ? 'positive' : 'negative');
  $('ticker-change-amount').textContent = (summary.change_24h >= 0 ? '+' : '') + money(summary.change_24h);
  $('ticker-high').textContent = money(summary.high_24h);
  $('ticker-low').textContent = money(summary.low_24h);
  $('ticker-volume').textContent = '$' + compact(summary.volume_24h_usd);
  $('ticker-coverage').textContent = 'Binance USDⓈ-M · 单一数据源';
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
  $(prefix + '-status').style.borderColor = status === 'triggered' ? '#2dd4bf' : status === 'approaching' ? '#fbbf24' : '#315064';
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

function mergeMarket(view, older, reset) {
  const incomingCandles = view.candles || [];
  const incomingSignals = view.model_signals || [];
  if (reset) {
    chart.candles = incomingCandles.slice();
    chart.signals = incomingSignals.slice();
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
    const firstIndex = oldFirst ? chart.candles.findIndex(item => item.time === oldFirst) : 0;
    chart.windowEnd = firstIndex + oldEnd;
  } else {
    const previousLast = chart.candles[chart.candles.length - 1]?.time;
    chart.candles = mergeCandles(chart.candles, incomingCandles);
    chart.signals = mergeSignals(chart.signals, incomingSignals);
    const latestChanged = previousLast && chart.candles[chart.candles.length - 1]?.time !== previousLast;
    if (chart.atLatest) chart.windowEnd = chart.candles.length;
    else if (latestChanged) chart.newData = true;
  }
  if (older || reset) {
    chart.hasMore = !!view.has_more;
    chart.nextBefore = view.next_before || '';
  }
  cache.market = view;
  $('new-data').classList.toggle('hidden', !chart.newData);
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
  [signal?.mark_price, signal?.upper_wall?.price, signal?.lower_wall?.price].forEach(value => { if (finite(value) && value > 0) values.push(value); });
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

function draw(view, map, signal, volume) {
  const svg = $('market-chart');
  svg.replaceChildren();
  const candles = visibleCandles();
  const bins = map?.bins || [];
  $('chart-title').textContent = (intervalName[chart.interval] || chart.interval) + ' K线 · 成交量分布 · 清算墙';
  const sessionRange = volume?.session_start ? ' (' + when(volume.session_start) + ' → ' + when(volume.session_end) + ')' : '';
  const profileState = volume?.state === 'ok' ? '当前时段成交量' + sessionRange : volume?.state === 'backfilling' ? '成交量回填 ' + ((volume.backfill_progress || 0) * 100).toFixed(0) + '%' + sessionRange : volume?.state === 'stale' ? '成交量已过期' + sessionRange : '成交量不可用';
  $('chart-context').textContent = (chart.atLatest ? '最新K线' : '历史K线') + ' · ' + profileState + ' / 当前清算墙 · 共用价格纵轴';
  if (!candles.length && !bins.length && !(volume?.bins || []).length) {
    svg.appendChild(svgNode('text', { x: 800, y: 280, 'text-anchor': 'middle', fill: '#83a0b2' }, '等待 K 线、成交量分布与清算地图数据'));
    return;
  }
  const { height, left, top, bottom, candleRight, volumeLeft, volumeRight, liquidationLeft, profileRight } = dims;
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
  }
  svg.appendChild(svgNode('line', { x1: volumeLeft - 18, y1: top, x2: volumeLeft - 18, y2: plotBottom, stroke: '#315064', 'stroke-width': 1 }));
  svg.appendChild(svgNode('line', { x1: liquidationLeft - 18, y1: top, x2: liquidationLeft - 18, y2: plotBottom, stroke: '#315064', 'stroke-width': 1 }));
  const plot = svgNode('g', { 'clip-path': 'url(#plot-clip)' });
  if (candles.length) {
    const step = (candleRight - left) / chart.visibleCount;
    const offset = chart.visibleCount - candles.length;
    const bodyWidth = Math.max(2, step * .58);
    const modelSignals = new Map();
    chart.signals.forEach(item => {
      const key = new Date(item.candle_time).getTime();
      if (!modelSignals.has(key)) modelSignals.set(key, []);
      modelSignals.get(key).push(item);
    });
    candles.forEach((candle, index) => {
      const x = left + (offset + index + .5) * step;
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
    for (let i = 0; i < 5; i++) {
      const index = Math.min(candles.length - 1, Math.round(i * (candles.length - 1) / 4));
      const x = left + (offset + index + .5) * step;
      const label = new Date(candles[index].time).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
      svg.appendChild(svgNode('text', { x, y: height - 17, 'text-anchor': 'middle', fill: '#7895a7', 'font-size': 11 }, label));
    }
  }
  if (bins.length) {
    const maximum = Math.max(...bins.map(item => item.total_usd), 1);
    const binWidth = map?.bin_width || (high - low) / Math.max(1, bins.length);
    const barHeight = Math.max(2, Math.abs(y(low + binWidth) - y(low)) * .9);
    bins.forEach(bin => {
      if (bin.price < low - binWidth || bin.price > high + binWidth) return;
      const width = Math.sqrt(bin.total_usd / maximum) * (profileRight - liquidationLeft);
      const color = bin.price < (map.mark_price || signal?.mark_price) ? '#fb7185' : '#2dd4bf';
      plot.appendChild(svgNode('rect', { x: liquidationLeft, y: y(bin.price) - barHeight / 2, width, height: barHeight, fill: color, opacity: .35 + .6 * bin.total_usd / maximum, 'data-tip': '当前清算墙快照\n价格 ' + money(bin.price) + '\n总清算强度 $' + compact(bin.total_usd) + '\n多仓 $' + compact(bin.long_usd) + '\n空仓 $' + compact(bin.short_usd) }));
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
      plot.appendChild(svgNode('rect', { x: volumeRight - width, y: y(center) - barHeight / 2, width, height: barHeight, fill: '#60a5fa', opacity: bin.in_value_area ? .88 : .28, 'data-tip': 'Binance 当前时段成交量\n价格 ' + money(bin.price_low) + ' – ' + money(bin.price_high) + '\n成交额 $' + compact(bin.volume_usd) + '\n占比 ' + (bin.volume_percent || 0).toFixed(2) + '%' + (bin.in_value_area ? '\n70%价值区域内' : '\n价值区域外') }));
    });
  }
  svg.appendChild(plot);
  svg.appendChild(svgNode('text', { x: (volumeLeft + volumeRight) / 2, y: top + 12, 'text-anchor': 'middle', fill: '#7dd3fc', 'font-size': 11, 'font-weight': 650 }, '当前时段成交量'));
  svg.appendChild(svgNode('text', { x: (liquidationLeft + profileRight) / 2, y: top + 12, 'text-anchor': 'middle', fill: '#83a0b2', 'font-size': 11, 'font-weight': 650 }, '当前清算墙'));
  const levels = [['现价', view?.summary?.last_price || signal?.mark_price, '#f8fafc', '4 5'], ['上墙', signal?.upper_wall?.price, '#2dd4bf', ''], ['下墙', signal?.lower_wall?.price, '#fb7185', '']];
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
    const specs = [['signal', '预测', 'signals/latest?symbol=' + symbol], ['map', '清算地图', 'map?symbol=' + symbol], ['volume', '成交量分布', 'volume-profile?symbol=' + symbol], ['market', '市场行情', marketPath()]];
    const results = await Promise.allSettled(specs.map(item => json(item[2])));
    if (requestedSymbol !== symbol || requestedInterval !== chart.interval || sequence !== refreshSequence) return;
    lastFailures = [];
    results.forEach((result, index) => {
      const [key, label] = specs[index];
      if (result.status === 'fulfilled') {
        if (key === 'market') mergeMarket(result.value, false, resetMarket || !chart.candles.length);
        else cache[key] = result.value;
      } else lastFailures.push(label + ' · ' + result.reason.message);
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
    if (point.x >= dims.left && point.x <= dims.candleRight) mode = 'time';
    else if (point.x >= dims.volumeLeft - 18 && point.x <= dims.profileRight) mode = 'price';
    if (!mode) return;
    chart.drag = { mode, pointerId: event.pointerId, x: point.x, y: point.y, end: chart.windowEnd, low: chart.yLow, high: chart.yHigh };
    svg.setPointerCapture(event.pointerId);
    wrap.classList.add('dragging');
  });
  svg.addEventListener('pointermove', event => {
    if (!chart.drag || chart.drag.pointerId !== event.pointerId) return;
    const point = svgPoint(event);
    if (chart.drag.mode === 'time') {
      const step = (dims.candleRight - dims.left) / chart.visibleCount;
      const shift = Math.round((chart.drag.x - point.x) / step);
      const minimum = Math.min(chart.visibleCount, chart.candles.length);
      chart.windowEnd = Math.max(minimum, Math.min(chart.candles.length, chart.drag.end + shift));
      chart.atLatest = chart.windowEnd === chart.candles.length;
      if (!chart.atLatest) chart.newData = false;
      $('new-data').classList.toggle('hidden', !chart.newData);
      if (!chart.yManual) chart.yLow = chart.yHigh = null;
      draw(cache.market, cache.map, cache.signal, cache.volume);
    } else {
      const span = chart.drag.high - chart.drag.low;
      const delta = (point.y - chart.drag.y) / (dims.height - dims.top - dims.bottom) * span;
      chart.yLow = chart.drag.low + delta;
      chart.yHigh = chart.drag.high + delta;
      chart.yManual = true;
      draw(cache.market, cache.map, cache.signal, cache.volume);
    }
  });
  const endDrag = event => {
    if (!chart.drag || chart.drag.pointerId !== event.pointerId) return;
    if (chart.drag.mode === 'time' && chart.windowEnd <= chart.visibleCount + 12) loadOlder();
    chart.drag = null;
    wrap.classList.remove('dragging');
  };
  svg.addEventListener('pointerup', endDrag);
  svg.addEventListener('pointercancel', endDrag);
}

function resetChartState() {
  chart.candles = [];
  chart.signals = [];
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
    localStorage.setItem('liquidation.interval', chart.interval);
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
setInterval(() => { if (!$('log-drawer').classList.contains('hidden') && $('log-auto').checked) loadLogs(false); }, 5000);

const events = new EventSource(api('stream'));
events.addEventListener('health', () => { streamError = ''; showFailures(lastFailures); });
events.addEventListener('prediction', event => {
  streamError = '';
  const prediction = JSON.parse(event.data);
  if (prediction.symbol === symbol) refresh(false);
});
events.onerror = () => { streamError = '连接中断，正在重试'; showFailures(lastFailures); };
