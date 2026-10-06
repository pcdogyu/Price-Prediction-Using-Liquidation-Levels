'use strict';

const NS = 'http://www.w3.org/2000/svg';
const $ = id => document.getElementById(id);
const cache = { signal: null, map: null, market: null };
let symbol = 'BTCUSDT';
let refreshing = false;
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
  if (!response.ok) {
    throw new Error('HTTP ' + response.status + ' · ' + (body?.detail || response.statusText || '请求失败'));
  }
  return body;
}

function updateTicker(view) {
  const summary = view?.summary;
  if (!summary) return;
  $('ticker-symbol').textContent = (view.symbol || symbol) + ' · 三所中位合成价';
  $('ticker-price').textContent = money(summary.last_price);
  $('ticker-updated').textContent = '更新 ' + when(summary.updated_at);
  const change = finite(summary.change_pct_24h) ? summary.change_pct_24h : 0;
  const up = change >= 0;
  $('ticker-change').textContent = (up ? '+' : '') + change.toFixed(2) + '%';
  $('ticker-change').className = 'value ' + (up ? 'positive' : 'negative');
  $('ticker-change-amount').textContent = (summary.change_24h >= 0 ? '+' : '') + money(summary.change_24h);
  $('ticker-high').textContent = money(summary.high_24h);
  $('ticker-low').textContent = money(summary.low_24h);
  $('ticker-volume').textContent = '$' + compact(summary.volume_24h_usd);
  $('ticker-coverage').textContent = summary.exchange_count + ' 个交易所覆盖';
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

function draw(view, map, signal) {
  const svg = $('market-chart');
  svg.replaceChildren();
  const candles = view?.candles || [];
  const bins = map?.bins || [];
  if (!candles.length && !bins.length) {
    svg.appendChild(svgNode('text', { x: 600, y: 280, 'text-anchor': 'middle', fill: '#83a0b2' }, '等待 K 线与清算地图数据'));
    return;
  }
  const height = 560, left = 72, top = 24, bottom = 42, candleRight = 850, profileLeft = 895, profileRight = 1165;
  const lows = candles.map(item => item.low).concat(bins.map(item => item.price));
  const highs = candles.map(item => item.high).concat(bins.map(item => item.price));
  let low = Math.min(...lows), high = Math.max(...highs);
  const padding = (high - low) * 0.025 || 1;
  low -= padding;
  high += padding;
  const y = price => top + (high - price) / (high - low) * (height - top - bottom);
  for (let i = 0; i < 6; i++) {
    const price = high - (high - low) * i / 5;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: '#18313f', 'stroke-width': 1 }));
    svg.appendChild(svgNode('text', { x: left - 8, y: yy + 4, 'text-anchor': 'end', fill: '#7895a7', 'font-size': 11 }, money(price)));
  }
  svg.appendChild(svgNode('line', { x1: profileLeft - 18, y1: top, x2: profileLeft - 18, y2: height - bottom, stroke: '#315064', 'stroke-width': 1 }));
  if (candles.length) {
    const step = (candleRight - left) / candles.length;
    const bodyWidth = Math.max(2, step * 0.58);
    candles.forEach((candle, index) => {
      const x = left + (index + 0.5) * step;
      const color = candle.close >= candle.open ? '#2dd4bf' : '#fb7185';
      const bodyTop = y(Math.max(candle.open, candle.close));
      const bodyBottom = y(Math.min(candle.open, candle.close));
      const patterns = (candle.patterns || []).map(item => patternName[item.name] || item.name).join(' · ') || '无识别形态';
      const group = svgNode('g', { 'data-tip': when(candle.time) + '\nO ' + money(candle.open) + '  H ' + money(candle.high) + '\nL ' + money(candle.low) + '  C ' + money(candle.close) + '\n成交额 $' + compact(candle.volume_usd) + '\n' + patterns });
      group.appendChild(svgNode('line', { x1: x, y1: y(candle.high), x2: x, y2: y(candle.low), stroke: color, 'stroke-width': 1.15 }));
      group.appendChild(svgNode('rect', { x: x - bodyWidth / 2, y: bodyTop, width: bodyWidth, height: Math.max(1.5, bodyBottom - bodyTop), fill: color, rx: 0.8 }));
      if (candle.patterns?.length) {
        const marker = candle.patterns.some(item => item.bias === 'bearish') ? '#fb7185' : candle.patterns.some(item => item.bias === 'bullish') ? '#2dd4bf' : '#fbbf24';
        group.appendChild(svgNode('circle', { cx: x, cy: Math.max(top + 5, y(candle.high) - 7), r: 2.7, fill: marker }));
      }
      svg.appendChild(group);
    });
    for (let i = 0; i < 5; i++) {
      const index = Math.min(candles.length - 1, Math.round(i * (candles.length - 1) / 4));
      const x = left + (index + 0.5) * (candleRight - left) / candles.length;
      const label = new Date(candles[index].time).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
      svg.appendChild(svgNode('text', { x, y: height - 17, 'text-anchor': 'middle', fill: '#7895a7', 'font-size': 11 }, label));
    }
  }
  if (bins.length) {
    const maximum = Math.max(...bins.map(item => item.total_usd), 1);
    const barHeight = Math.max(2, (height - top - bottom) / bins.length * 0.9);
    bins.forEach(bin => {
      const width = Math.sqrt(bin.total_usd / maximum) * (profileRight - profileLeft);
      const color = bin.price < (map.mark_price || signal?.mark_price) ? '#fb7185' : '#2dd4bf';
      svg.appendChild(svgNode('rect', { x: profileLeft, y: y(bin.price) - barHeight / 2, width, height: barHeight, fill: color, opacity: 0.35 + 0.6 * bin.total_usd / maximum, 'data-tip': '价格 ' + money(bin.price) + '\n总清算强度 $' + compact(bin.total_usd) + '\n多仓 $' + compact(bin.long_usd) + '\n空仓 $' + compact(bin.short_usd) }));
    });
  }
  const levels = [['现价', view?.summary?.last_price || signal?.mark_price, '#f8fafc', '4 5'], ['上墙', signal?.upper_wall?.price, '#2dd4bf', ''], ['下墙', signal?.lower_wall?.price, '#fb7185', '']];
  levels.forEach(([name, price, color, dash]) => {
    if (!price || price < low || price > high) return;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: color, 'stroke-width': name === '现价' ? 1.2 : 1.8, 'stroke-dasharray': dash, opacity: 0.95 }));
    svg.appendChild(svgNode('rect', { x: profileRight - 112, y: yy - 12, width: 112, height: 22, fill: '#071019', stroke: color, rx: 5 }));
    svg.appendChild(svgNode('text', { x: profileRight - 6, y: yy + 4, 'text-anchor': 'end', fill: color, 'font-size': 11 }, name + ' ' + money(price)));
  });
  bindTips(svg);
}

function bindTips(svg) {
  const tooltip = $('tooltip');
  svg.querySelectorAll('[data-tip]').forEach(element => {
    element.addEventListener('mousemove', event => {
      tooltip.textContent = element.getAttribute('data-tip');
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(innerWidth - 230, event.clientX + 14) + 'px';
      tooltip.style.top = Math.min(innerHeight - 120, event.clientY + 14) + 'px';
    });
    element.addEventListener('mouseleave', () => { tooltip.style.display = 'none'; });
  });
}

function showFailures(failures) {
  const all = [...failures];
  if (streamError) all.push('实时推送 · ' + streamError);
  $('api-errors').textContent = all.length ? '数据接口异常：\n' + all.join('\n') : '';
  $('api-errors').classList.toggle('hidden', all.length === 0);
}

async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    const specs = [['signal', '预测', 'signals/latest?symbol=' + symbol], ['map', '清算地图', 'map?symbol=' + symbol], ['market', '市场行情', 'market?symbol=' + symbol]];
    const results = await Promise.allSettled(specs.map(item => json(item[2])));
    lastFailures = [];
    results.forEach((result, index) => {
      const [key, label] = specs[index];
      if (result.status === 'fulfilled') cache[key] = result.value;
      else lastFailures.push(label + ' · ' + result.reason.message);
    });
    if (cache.market) updateTicker(cache.market);
    if (cache.signal) {
      updateProbabilities(cache.signal.probabilities);
      updateTrigger('upper', cache.signal.upper_trigger);
      updateTrigger('lower', cache.signal.lower_trigger);
      $('reason').textContent = cache.signal.state === 'ok' ? '当前领先类别：' + (cache.signal.leading_class || '—') + '；触发顺序：' + (cache.signal.trigger_order || 'pending') + '。' : (cache.signal.reason || '清算墙或历史数据不足');
      $('model-info').textContent = '模型：' + (cache.signal.model_version || '尚未训练') + ' · 数据年龄 ' + (finite(cache.signal.data_age_seconds) ? Math.max(0, cache.signal.data_age_seconds).toFixed(0) + ' 秒' : '—');
    }
    draw(cache.market, cache.map, cache.signal);
    showFailures(lastFailures);
  } finally {
    refreshing = false;
  }
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

function openLogs() {
  $('log-drawer').classList.remove('hidden');
  $('drawer-backdrop').classList.remove('hidden');
  loadLogs(false);
}

function closeLogs() {
  $('log-drawer').classList.add('hidden');
  $('drawer-backdrop').classList.add('hidden');
}

document.querySelectorAll('button[data-symbol]').forEach(button => {
  button.addEventListener('click', () => {
    document.querySelectorAll('button[data-symbol]').forEach(item => item.classList.remove('active'));
    button.classList.add('active');
    symbol = button.dataset.symbol;
    cache.signal = cache.map = cache.market = null;
    refresh();
  });
});
$('log-button').addEventListener('click', openLogs);
$('log-close').addEventListener('click', closeLogs);
$('drawer-backdrop').addEventListener('click', closeLogs);
$('log-refresh').addEventListener('click', () => loadLogs(false));
$('log-more').addEventListener('click', () => loadLogs(true));
$('log-level').addEventListener('change', () => { logCursor = ''; loadLogs(false); });
$('log-copy').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(logEntries.map(formatLog).join('\n'));
  } catch (_) {
    $('log-error').textContent = '浏览器不允许复制日志';
    $('log-error').classList.remove('hidden');
  }
});

refresh();
setInterval(refresh, 10000);
setInterval(() => {
  if (!$('log-drawer').classList.contains('hidden') && $('log-auto').checked) loadLogs(false);
}, 5000);

const events = new EventSource(api('stream'));
events.addEventListener('health', () => { streamError = ''; showFailures(lastFailures); });
events.addEventListener('prediction', event => {
  streamError = '';
  const prediction = JSON.parse(event.data);
  if (prediction.symbol === symbol) refresh();
});
events.onerror = () => { streamError = '连接中断，正在重试'; showFailures(lastFailures); };
