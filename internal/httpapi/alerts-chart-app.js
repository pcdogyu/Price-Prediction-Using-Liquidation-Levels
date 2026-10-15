/* The per pair K-line page. All chart math lives in alerts-chart.js so the pure
   parts stay unit testable; this file only wires DOM, canvas and polling. */
(function () {
  'use strict';
  const K = window.LiquidationKline;
  const $ = id => document.getElementById(id);
  const base = (document.querySelector('meta[name="app-base"]') || {}).content || '/';
  const escape = value => String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
  const finite = value => typeof value === 'number' && Number.isFinite(value);
  const stored = (key, fallback) => { try { const value = localStorage.getItem(key); return value === null ? fallback : value === '1'; } catch (error) { return fallback; } };
  const remember = (key, value) => { try { localStorage.setItem(key, value ? '1' : '0'); } catch (error) { /* private mode keeps defaults */ } };
  const storedText = (key, fallback) => { try { return localStorage.getItem(key) || fallback; } catch (error) { return fallback; } };
  const rememberText = (key, value) => { try { localStorage.setItem(key, value); } catch (error) { /* private mode keeps defaults */ } };

  const STORE_SIGNALS = 'liquidation.kline.signals', STORE_ALERTS = 'liquidation.kline.alerts', STORE_AGGRESSIVE = 'liquidation.kline.aggressive', STORE_STEADY = 'liquidation.kline.steady';
  const CANDLE_REFRESH_MS = 5000, SIGNAL_REFRESH_MS = 15000;
  const params = new URLSearchParams(location.search);
  const wantedInterval = (params.get('interval') || '').toLowerCase();
  const priceText = value => finite(value) && value > 0 ? (value >= 1000 ? value.toFixed(2) : value >= 1 ? value.toFixed(4) : value.toFixed(6)) : '—';
  const money = value => !finite(value) ? '—' : value >= 1e9 ? '$' + (value / 1e9).toFixed(2) + 'B' : value >= 1e6 ? '$' + (value / 1e6).toFixed(2) + 'M' : value >= 1e3 ? '$' + (value / 1e3).toFixed(1) + 'K' : '$' + value.toFixed(2);
  const clock = at => new Date(at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });

  const state = {
    symbol: (params.get('symbol') || '').toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 32),
    interval: K.INTERVALS.includes(wantedInterval) ? wantedInterval : (K.INTERVALS.includes(storedText('liquidation.kline.interval', '')) ? storedText('liquidation.kline.interval', '15m') : '15m'),
    candles: [], alerts: [], signals: [], hits: [],
    view: { start: 0, end: 0 }, yLow: 0, yHigh: 1, yManual: false,
    hasMore: true, nextBefore: '', newest: 0, atLatest: true, newData: false, keepSpan: 0,
    loading: false, loadingHistory: false, loadingSignals: false, loadingAlerts: false,
    drag: null, hoverCandle: null, hoverMarker: null,
    toggles: { alerts: stored(STORE_ALERTS, true), signals: stored(STORE_SIGNALS, true), aggressive: stored(STORE_AGGRESSIVE, true), steady: stored(STORE_STEADY, true) }
  };
  let dims = null, colors = {}, scales = null;

  const step = () => K.intervalMs(state.interval);
  const newestCandle = () => state.candles.length ? state.candles[state.candles.length - 1] : null;
  const chartLimits = () => {
    const span = state.view.end - state.view.start || K.DEFAULT_VISIBLE * step();
    const first = state.candles.length ? state.candles[0].t : Date.now();
    const last = state.candles.length ? state.candles[state.candles.length - 1].t : Date.now();
    return { minSpan: K.MIN_VISIBLE * step(), maxSpan: K.MAX_VISIBLE * step(), minStart: first - span, maxEnd: last + (K.RIGHT_PAD + 1) * step() };
  };
  const signalOptions = () => ({ aggressive: state.toggles.aggressive, steady: state.toggles.steady });
  const shownAlerts = () => state.toggles.alerts ? K.filterAlerts(state.alerts) : [];
  const shownSignals = () => state.toggles.signals ? K.filterSignals(state.signals, signalOptions()) : [];

  async function json(path) {
    const response = await fetch(base + path, { cache: 'no-store', credentials: 'same-origin' });
    if (response.status === 401) { location.assign(base); throw new Error('需要重新登录'); }
    const packet = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
    return packet;
  }

  function showError(message) { $('kline-error').textContent = message; $('kline-error').hidden = false; }
  function clearError() { $('kline-error').hidden = true; $('kline-error').textContent = ''; }
  function note(message) { $('kline-status').textContent = message; }

  function pageUrl(symbol, interval) {
    return base + 'alerts/chart?symbol=' + encodeURIComponent(symbol) + '&interval=' + encodeURIComponent(interval);
  }

  // buildIntervals renders the nine period buttons; switching keeps the visible
  // time span so the same window is refetched at the new resolution.
  function buildIntervals() {
    $('kline-intervals').innerHTML = K.INTERVALS.map(name => '<button type="button" data-interval="' + name + '">' + name + '</button>').join('');
    $('kline-intervals').addEventListener('click', event => {
      const button = event.target.closest('button[data-interval]');
      if (!button || button.dataset.interval === state.interval) return;
      state.keepSpan = state.view.end - state.view.start;
      state.interval = button.dataset.interval;
      state.candles = []; state.hasMore = true; state.nextBefore = ''; state.yManual = false; state.atLatest = true; state.newData = false;
      rememberText('liquidation.kline.interval', state.interval);
      syncIntervals();
      history.replaceState(null, '', pageUrl(state.symbol, state.interval));
      note('正在切换到 ' + state.interval + ' K 线');
      loadCandles('initial');
    });
  }
  function syncIntervals() {
    document.querySelectorAll('#kline-intervals button').forEach(button => button.classList.toggle('selected', button.dataset.interval === state.interval));
  }

  // candleLimit sizes a request so the fetched page covers the window the chart
  // shows plus enough history to pan into without an immediate refetch.
  function candleLimit(mode) {
    if (mode === 'history') return 1000;
    const wanted = state.keepSpan > 0 ? state.keepSpan : K.DEFAULT_VISIBLE * step();
    return Math.max(120, Math.min(1000, Math.ceil(wanted / step()) + 80));
  }

  function initialWindow() {
    const last = newestCandle();
    if (!last) return { start: 0, end: 0 };
    const span = state.keepSpan > 0 ? state.keepSpan : K.DEFAULT_VISIBLE * step();
    const end = last.t + (K.RIGHT_PAD + 1) * step();
    return K.clampWindow({ start: end - span, end }, chartLimits());
  }

  function atLatest() {
    const last = newestCandle();
    if (!last) return true;
    const span = state.view.end - state.view.start;
    return state.view.end >= last.t + (K.RIGHT_PAD + 1) * step() - span * .02;
  }

  function statusText() {
    const last = newestCandle();
    const parts = [state.candles.length + ' 根 ' + state.interval + ' K 线'];
    if (last) parts.push('最新 ' + clock(last.t));
    parts.push(state.hasMore ? '左拉可读取更早数据' : '已到最早数据');
    parts.push(shownAlerts().length + ' 个预警');
    parts.push(shownSignals().length + ' 个下单信号');
    parts.push(state.atLatest ? '每 5 秒自动刷新' : '已暂停跟随最新');
    return parts.join(' · ');
  }

  async function loadCandles() {
    if (state.loading || !state.symbol) return;
    state.loading = true;
    try {
      const packet = await json('api/v1/alerts/klines?symbol=' + encodeURIComponent(state.symbol) + '&interval=' + encodeURIComponent(state.interval) + '&limit=' + candleLimit('latest'));
      applyCandles(packet);
      clearError();
      note(statusText());
    } catch (error) {
      showError('K 线读取失败：' + error.message + '（币需为 Binance USDⓈ-M 永续合约名，如 BTCUSDT）');
      note('K 线读取失败，5 秒后重试');
    } finally {
      state.loading = false;
      state.keepSpan = 0;
      renderReadout();
      draw();
    }
  }

  // applyCandles merges a page and decides what the window does: a fresh load fits
  // the newest candles, a refresh follows the newest candle only while the user is
  // parked at the right edge, and a panned-back view stays exactly where it was.
  function applyCandles(packet) {
    state.candles = K.mergeCandles(state.candles, K.normaliseCandles(packet.candles));
    state.hasMore = packet.has_more === true;
    state.nextBefore = packet.next_before || '';
    const last = newestCandle();
    if (!last) return;
    if (last.t > state.newest && !state.atLatest) state.newData = true;
    state.newest = last.t;
    const span = state.view.end - state.view.start;
    if (!span) state.view = initialWindow();
    else if (state.atLatest) state.view = K.clampWindow({ start: last.t + (K.RIGHT_PAD + 1) * step() - span, end: last.t + (K.RIGHT_PAD + 1) * step() }, chartLimits());
    else state.view = K.clampWindow(state.view, chartLimits());
    state.atLatest = atLatest();
    $('kline-new-data').classList.toggle('hidden', !state.newData);
  }

  // loadHistory pulls an older page once the view nears the oldest loaded candle,
  // which is what lets the chart be dragged back through time.
  function loadHistory() {
    if (state.loading || state.loadingHistory || !state.hasMore || !state.nextBefore || !state.candles.length) return;
    if (state.view.start > state.candles[0].t + K.HISTORY_TRIGGER * step()) return;
    state.loadingHistory = true;
    note('正在读取更早的 K 线');
    json('api/v1/alerts/klines?symbol=' + encodeURIComponent(state.symbol) + '&interval=' + encodeURIComponent(state.interval) + '&limit=1000&before=' + encodeURIComponent(state.nextBefore))
      .then(packet => {
        state.candles = K.mergeCandles(state.candles, K.normaliseCandles(packet.candles));
        state.hasMore = packet.has_more === true;
        state.nextBefore = packet.next_before || '';
      })
      .catch(error => showError('更早 K 线读取失败：' + error.message))
      .finally(() => { state.loadingHistory = false; note(statusText()); draw(); });
  }

  // loadSignals reads the stored trade signals for this pair, paging back until
  // the loaded range covers the visible chart window or three pages are read.
  async function loadSignals() {
    if (state.loadingSignals || !state.symbol) return;
    state.loadingSignals = true;
    try {
      const rows = [];
      let cursor = '', pages = 0;
      do {
        const packet = await json('api/v1/alerts/signals?symbol=' + encodeURIComponent(state.symbol) + '&limit=200' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''));
        rows.push(...(packet.rows || []));
        cursor = packet.next_cursor || '';
        pages += 1;
        const times = rows.map(row => Date.parse(row.created_at)).filter(Number.isFinite);
        if (!times.length || !state.view.start || times.reduce((oldest, at) => Math.min(oldest, at), Infinity) < state.view.start) break;
      } while (cursor && pages < 3);
      state.signals = rows.map(row => ({ id: row.id, t: Date.parse(row.created_at), kind: row.kind, side: row.side,
        reference_price: row.reference_price, alert_notional_usd: row.alert_notional_usd, notify_status: row.notify_status })).filter(row => Number.isFinite(row.t));
      $('kline-signal-status').textContent = '已读取 ' + state.signals.length + ' 条 · 每 15 秒刷新';
    } catch (error) {
      $('kline-signal-status').textContent = '信号读取失败：' + error.message;
    } finally {
      state.loadingSignals = false;
      renderSignals();
      draw();
    }
  }

  // loadAlerts mirrors the alerts page history feed and keeps enough pages to
  // cover the visible chart window. The chart uses triggered_at because that is
  // the timestamp shown for a warning on the alerts page.
  async function loadAlerts() {
    if (state.loadingAlerts || !state.symbol) return;
    state.loadingAlerts = true;
    try {
      const rows = [];
      let cursor = '', pages = 0;
      do {
        const packet = await json('api/v1/alerts/history?symbol=' + encodeURIComponent(state.symbol) + '&limit=200' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''));
        rows.push(...(packet.rows || []));
        cursor = packet.next_cursor || '';
        pages += 1;
        const times = rows.map(row => Date.parse(row.triggered_at)).filter(Number.isFinite);
        if (!times.length || !state.view.start || times.reduce((oldest, at) => Math.min(oldest, at), Infinity) < state.view.start) break;
      } while (cursor && pages < 3);
      state.alerts = rows.map(row => ({ id: row.id, t: Date.parse(row.triggered_at), side: row.trade_side || row.side,
        liquidation_side: row.side, reference_price: row.reference_price, notional_usd: row.notional_usd,
        threshold_usd: row.threshold_usd, status: row.status })).filter(row => Number.isFinite(row.t));
      $('kline-alert-status').textContent = '已读取 ' + state.alerts.length + ' 条 · 每 15 秒刷新';
    } catch (error) {
      $('kline-alert-status').textContent = '预警读取失败：' + error.message;
    } finally {
      state.loadingAlerts = false;
      renderSignals();
      draw();
    }
  }

  function renderSignals() {
    const notifyLabels = { sent: '已推送', suppressed: '已抑制', skipped: '未启用', disabled: '已关闭', invalid: '配置无效', failed: '发送失败' };
    const rows = shownSignals().slice().reverse().slice(0, 200);
    if (!rows.length) {
      $('kline-signal-rows').innerHTML = '<tr><td colspan="6">' + (state.toggles.signals ? '该币对暂无下单信号' : '信号图层已隐藏') + '</td></tr>';
      return;
    }
    $('kline-signal-rows').innerHTML = rows.map(row => '<tr>' +
      '<td>' + escape(clock(row.t)) + '</td>' +
      '<td class="' + (row.kind === 'steady' ? 'buy' : 'warning') + '">' + (row.kind === 'steady' ? '稳健' : '激进') + '</td>' +
      '<td class="' + (row.side === 'short' ? 'sell' : 'buy') + '">' + (row.side === 'short' ? '做空' : '做多') + '</td>' +
      '<td>' + escape(priceText(row.reference_price)) + '</td>' +
      '<td>' + escape(money(row.alert_notional_usd)) + '</td>' +
      '<td>' + escape(notifyLabels[row.notify_status] || row.notify_status || '未推送') + '</td></tr>').join('');
  }

  function renderReadout() {
    const candle = state.hoverCandle || newestCandle();
    if (!candle) { $('kline-readout').textContent = '等待 K 线数据'; return; }
    const change = candle.open > 0 ? (candle.close / candle.open - 1) * 100 : 0;
    const markerText = state.hoverMarker ? (state.hoverMarker.type === 'alert'
      ? ' · 悬停预警：' + (state.hoverMarker.side === 'short' ? '做空' : '做多') + ' ×' + state.hoverMarker.count
      : ' · 悬停信号：' + escape(state.hoverMarker.kind === 'steady' ? '稳健' : '激进') +
        (state.hoverMarker.side === 'short' ? '做空' : '做多') + ' ×' + state.hoverMarker.count) : '';
    $('kline-readout').innerHTML = '<b>' + escape(state.symbol) + ' · ' + escape(state.interval) + '</b> ' + escape(clock(candle.t)) +
      ' 开 ' + escape(priceText(candle.open)) + ' 高 ' + escape(priceText(candle.high)) + ' 低 ' + escape(priceText(candle.low)) +
      ' 收 ' + escape(priceText(candle.close)) + ' <span class="' + (change >= 0 ? 'buy' : 'sell') + '">' + (change >= 0 ? '+' : '') + change.toFixed(2) + '%</span>' +
      ' 成交额 ' + escape(money(candle.volume)) + markerText;
  }

  // palette reads the theme variables so the canvas follows the shared light and
  // dark theme instead of hard coding colours.
  function palette() {
    const styles = getComputedStyle(document.documentElement);
    const read = name => styles.getPropertyValue(name).trim();
    colors = { bg: read('--chart-bg'), grid: read('--chart-grid'), text: read('--text'), muted: read('--muted'),
      buy: read('--buy'), sell: read('--sell'), warning: read('--warning'), blue: read('--blue'), line: read('--line') };
  }

  // draw paints grid, volume band, candles, the newest price line and the signal
  // layer, and records the drawn markers so the pointer can hit test them.
  function draw() {
    if (!dims) return;
    const ctx = $('kline-chart').getContext('2d');
    const { width, height, ratio, plot, priceBottom, volumeTop } = dims;
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    ctx.fillStyle = colors.bg;
    ctx.fillRect(0, 0, width, height);
    state.hits = [];
    if (!state.candles.length || !state.view.end) {
      notice(ctx, width, height, state.symbol ? '等待 K 线数据' : '缺少币对参数，请从报警页点击币对打开');
      renderReadout();
      return;
    }
    const intervalStep = step();
    const auto = state.yManual ? null : K.priceRange(state.candles, state.view, intervalStep);
    if (auto) { state.yLow = auto[0]; state.yHigh = auto[1]; }
    if (!finite(state.yLow) || !finite(state.yHigh) || state.yHigh <= state.yLow) { state.yLow = 0; state.yHigh = 1; }
    const low = state.yLow, high = state.yHigh, span = state.view.end - state.view.start;
    const x = at => plot.left + (at - state.view.start) / span * plot.width;
    const y = price => plot.top + (high - price) / (high - low) * (priceBottom - plot.top);
    scales = { x, y, timeAt: px => state.view.start + (px - plot.left) / plot.width * span, priceAt: py => high - (py - plot.top) / (priceBottom - plot.top) * (high - low) };
    ctx.font = '11px system-ui, "Segoe UI", sans-serif';
    ctx.textBaseline = 'middle';
    ctx.lineWidth = 1;
    ctx.textAlign = 'left';
    for (const tick of K.niceTicks(low, high, 6)) {
      const py = Math.round(y(tick)) + .5;
      if (py < plot.top || py > priceBottom) continue;
      ctx.strokeStyle = colors.grid;
      ctx.beginPath(); ctx.moveTo(plot.left, py); ctx.lineTo(plot.right, py); ctx.stroke();
      ctx.fillStyle = colors.muted;
      ctx.fillText(priceText(tick), plot.right + 7, py);
    }
    ctx.textAlign = 'center';
    const labels = Math.max(3, Math.min(8, Math.floor(plot.width / 130)));
    for (let index = 0; index <= labels; index += 1) {
      const at = state.view.start + span * index / labels, px = Math.round(x(at)) + .5;
      ctx.strokeStyle = colors.grid;
      ctx.beginPath(); ctx.moveTo(px, plot.top); ctx.lineTo(px, plot.bottom); ctx.stroke();
      ctx.fillStyle = colors.muted;
      ctx.fillText(clock(at), px, plot.bottom + 12);
    }
    const shown = K.visibleCandles(state.candles, state.view, intervalStep);
    const slot = plot.width / Math.max(1, span / intervalStep), body = Math.max(1, Math.min(18, slot * .68));
    const peak = K.volumeMax(state.candles, state.view, intervalStep);
    for (const candle of shown) {
      const cx = x(candle.t + intervalStep / 2);
      if (cx < plot.left - 12 || cx > plot.right + 12) continue;
      const up = candle.close >= candle.open, colour = up ? colors.buy : colors.sell;
      if (peak > 0) {
        const bar = Math.max(1, candle.volume / peak * (plot.bottom - volumeTop));
        ctx.globalAlpha = .4;
        ctx.fillStyle = colors.blue;
        ctx.fillRect(cx - body / 2, plot.bottom - bar, body, bar);
        ctx.globalAlpha = 1;
      }
      ctx.strokeStyle = colour;
      ctx.beginPath(); ctx.moveTo(cx, y(candle.high)); ctx.lineTo(cx, y(candle.low)); ctx.stroke();
      const top = y(Math.max(candle.open, candle.close)), bottom = y(Math.min(candle.open, candle.close));
      ctx.fillStyle = colour;
      ctx.fillRect(cx - body / 2, top, body, Math.max(1, bottom - top));
    }
    drawAlerts(ctx, shown, x, y, plot, priceBottom, intervalStep);
    drawSignals(ctx, shown, x, y, plot, priceBottom, intervalStep);
    drawLastPrice(ctx, y, plot, priceBottom);
    if (state.hoverCandle) drawCrosshair(ctx, x, plot, priceBottom);
    renderReadout();
  }

  // drawAlerts paints warning diamonds below the signal layer. They use the
  // trade direction from the alert page and remain visually distinct from the
  // ▲/▼ order markers.
  function drawAlerts(ctx, shown, x, y, plot, priceBottom, intervalStep) {
    if (!state.toggles.alerts) return;
    const byTime = new Map(shown.map(candle => [candle.t, candle]));
    for (const group of K.alertGroups(shownAlerts(), intervalStep)) {
      const cx = x(group.bucket + intervalStep / 2);
      if (cx < plot.left - 10 || cx > plot.right + 10) continue;
      const candle = byTime.get(group.bucket) || nearest(shown, group.bucket);
      const price = group.price > 0 ? group.price : candle ? candle.close : 0;
      if (!price) continue;
      const long = group.side !== 'short';
      const py = Math.max(plot.top + 10, Math.min(priceBottom - 10, y(price) + (long ? 32 : -32)));
      diamond(ctx, cx, py, colors.warning);
      if (group.count > 1) {
        ctx.fillStyle = colors.text;
        ctx.textAlign = 'left';
        ctx.fillText('×' + group.count, cx + 10, py);
      }
      state.hits.push({ x: cx, y: py, group });
    }
  }

  function notice(ctx, width, height, message) {
    ctx.fillStyle = colors.muted;
    ctx.font = '14px system-ui, "Segoe UI", sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(message, width / 2, height / 2);
  }

  // drawSignals paints one marker per candle, kind and direction. Steady signals
  // are filled, aggressive ones stay hollow, and a badge counts repeated signals.
  function drawSignals(ctx, shown, x, y, plot, priceBottom, intervalStep) {
    if (!state.toggles.signals) return;
    const byTime = new Map(shown.map(candle => [candle.t, candle]));
    for (const group of K.markerGroups(shownSignals(), intervalStep)) {
      const cx = x(group.bucket + intervalStep / 2);
      if (cx < plot.left - 10 || cx > plot.right + 10) continue;
      const candle = byTime.get(group.bucket) || nearest(shown, group.bucket);
      const price = group.price > 0 ? group.price : candle ? candle.close : 0;
      if (!price) continue;
      const long = group.side !== 'short';
      const py = Math.max(plot.top + 10, Math.min(priceBottom - 10, y(price) + (long ? 16 : -16)));
      triangle(ctx, cx, py, long, group.kind === 'steady', long ? colors.buy : colors.sell);
      if (group.count > 1) {
        ctx.fillStyle = colors.text;
        ctx.textAlign = 'left';
        ctx.fillText('×' + group.count, cx + 10, py);
      }
      state.hits.push({ x: cx, y: py, group: Object.assign({ type: 'signal' }, group) });
    }
  }

  function nearest(candles, at) {
    let best = null, distance = Infinity;
    for (const candle of candles) {
      const gap = Math.abs(candle.t - at);
      if (gap < distance) { distance = gap; best = candle; }
    }
    return best;
  }

  function triangle(ctx, cx, cy, pointUp, filled, colour) {
    const size = 7, apex = pointUp ? cy - size : cy + size, base = pointUp ? cy + size * .7 : cy - size * .7;
    ctx.beginPath();
    ctx.moveTo(cx, apex);
    ctx.lineTo(cx - size, base);
    ctx.lineTo(cx + size, base);
    ctx.closePath();
    if (filled) { ctx.fillStyle = colour; ctx.fill(); return; }
    ctx.fillStyle = colors.bg;
    ctx.fill();
    ctx.strokeStyle = colour;
    ctx.lineWidth = 2;
    ctx.stroke();
    ctx.lineWidth = 1;
  }

  function diamond(ctx, cx, cy, colour) {
    const size = 7;
    ctx.save();
    ctx.beginPath();
    ctx.moveTo(cx, cy - size);
    ctx.lineTo(cx + size, cy);
    ctx.lineTo(cx, cy + size);
    ctx.lineTo(cx - size, cy);
    ctx.closePath();
    ctx.fillStyle = colors.bg;
    ctx.fill();
    ctx.strokeStyle = colour;
    ctx.lineWidth = 2;
    ctx.stroke();
    ctx.fillStyle = colour;
    ctx.font = 'bold 10px system-ui, "Segoe UI", sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('!', cx, cy + .5);
    ctx.restore();
  }

  function drawLastPrice(ctx, y, plot, priceBottom) {
    const last = newestCandle();
    if (!last) return;
    const py = y(last.close);
    if (py < plot.top || py > priceBottom) return;
    const colour = last.close >= last.open ? colors.buy : colors.sell;
    ctx.save();
    ctx.setLineDash([4, 4]);
    ctx.strokeStyle = colour;
    ctx.beginPath(); ctx.moveTo(plot.left, py); ctx.lineTo(plot.right, py); ctx.stroke();
    ctx.restore();
    ctx.fillStyle = colour;
    ctx.textAlign = 'left';
    ctx.fillText(priceText(last.close), plot.right + 7, py);
  }

  function drawCrosshair(ctx, x, plot, priceBottom) {
    const cx = x(state.hoverCandle.t + step() / 2);
    ctx.save();
    ctx.setLineDash([3, 3]);
    ctx.strokeStyle = colors.muted;
    ctx.beginPath(); ctx.moveTo(cx, plot.top); ctx.lineTo(cx, plot.bottom); ctx.stroke();
    if (finite(state.hoverY) && state.hoverY >= plot.top && state.hoverY <= priceBottom) {
      ctx.beginPath(); ctx.moveTo(plot.left, state.hoverY); ctx.lineTo(plot.right, state.hoverY); ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = colors.muted;
      ctx.textAlign = 'left';
      ctx.fillText(priceText(scales.priceAt(state.hoverY)), plot.right + 7, state.hoverY);
    }
    ctx.restore();
  }

  // layout splits the canvas into the price area, the volume band and the axes.
  function layout(width, height) {
    const margins = { left: 10, right: 80, top: 14, bottom: 26 };
    const plot = { left: margins.left, top: margins.top, width: Math.max(60, width - margins.left - margins.right), height: Math.max(140, height - margins.top - margins.bottom) };
    plot.right = plot.left + plot.width;
    plot.bottom = plot.top + plot.height;
    const volumeHeight = Math.max(34, Math.min(90, plot.height * .18));
    return { plot, priceBottom: plot.bottom - volumeHeight - 8, volumeTop: plot.bottom - volumeHeight };
  }

  // resize maps the drawing buffer to the device pixels so the candles stay crisp.
  function resize() {
    const wrap = $('kline-wrap'), canvas = $('kline-chart');
    const ratio = window.devicePixelRatio || 1;
    const width = Math.max(320, wrap.clientWidth), height = Math.max(360, Math.min(660, Math.round(window.innerHeight * .58)));
    canvas.style.width = width + 'px';
    canvas.style.height = height + 'px';
    canvas.width = Math.round(width * ratio);
    canvas.height = Math.round(height * ratio);
    dims = Object.assign({ width, height, ratio }, layout(width, height));
    draw();
  }

  function pointer(event) {
    const rect = $('kline-chart').getBoundingClientRect();
    return { x: event.clientX - rect.left, y: event.clientY - rect.top };
  }
  const inPlot = at => !!dims && at.x >= dims.plot.left && at.x <= dims.plot.right && at.y >= dims.plot.top && at.y <= dims.plot.bottom;

  // hover tracks the candle under the pointer and, within a marker radius, that
  // marker, so the readout and the tooltip answer what is pointed at.
  function hover(at) {
    if (!scales) return;
    const wasCandle = state.hoverCandle ? state.hoverCandle.t : 0, wasMarker = state.hoverMarker ? state.hoverMarker.bucket : 0;
    state.hoverY = at.y;
    state.hoverCandle = inPlot(at) && state.candles.length ? nearest(state.candles, scales.timeAt(at.x)) : null;
    state.hoverMarker = null;
    for (const hit of state.hits) {
      if (Math.abs(hit.x - at.x) <= 11 && Math.abs(hit.y - at.y) <= 11) { state.hoverMarker = hit.group; break; }
    }
    showTooltip(at);
    if ((state.hoverCandle ? state.hoverCandle.t : 0) !== wasCandle || (state.hoverMarker ? state.hoverMarker.bucket : 0) !== wasMarker) draw();
    else renderReadout();
  }

  function showTooltip(at) {
    const tooltip = $('kline-tooltip'), wrap = $('kline-wrap');
    if (!state.hoverMarker || !dims) { tooltip.hidden = true; return; }
    const marker = state.hoverMarker;
    if (marker.type === 'alert') {
      tooltip.innerHTML = '<b>预警 · ' + escape(marker.side === 'short' ? '做空' : '做多') + '</b><br>' +
        escape(clock(marker.first)) + (marker.count > 1 ? ' 起 · 共 ' + marker.count + ' 条' : '') + '<br>清算方向 ' +
        escape(marker.liquidationSide === 'short' ? '空单爆仓' : '多单爆仓') + '<br>参考价 ' + escape(priceText(marker.price)) +
        '<br>预警爆仓额 ' + escape(money(marker.notionalUSD));
    } else {
      tooltip.innerHTML = '<b>' + escape(marker.kind === 'steady' ? '稳健信号' : '激进信号') + ' · ' + escape(marker.side === 'short' ? '做空' : '做多') + '</b><br>' +
        escape(clock(marker.first)) + (marker.count > 1 ? ' 起 · 共 ' + marker.count + ' 条' : '') + '<br>参考价 ' + escape(priceText(marker.price));
    }
    tooltip.hidden = false;
    tooltip.style.left = Math.max(6, Math.min(wrap.clientWidth - tooltip.offsetWidth - 6, at.x + 14)) + 'px';
    tooltip.style.top = Math.max(6, Math.min(dims.height - tooltip.offsetHeight - 6, at.y + 12)) + 'px';
  }

  function bindCanvas() {
    const canvas = $('kline-chart'), wrap = $('kline-wrap');
    canvas.addEventListener('pointerdown', event => {
      const at = pointer(event);
      if (!inPlot(at) || !state.candles.length) return;
      canvas.setPointerCapture(event.pointerId);
      state.drag = { id: event.pointerId, x: at.x, y: at.y, view: { start: state.view.start, end: state.view.end }, low: state.yLow, high: state.yHigh };
      wrap.classList.add('dragging');
      $('kline-tooltip').hidden = true;
      event.preventDefault();
    });
    canvas.addEventListener('pointermove', event => {
      const at = pointer(event);
      if (state.drag && state.drag.id === event.pointerId) panTo(at);
      else hover(at);
    });
    const release = event => {
      if (!state.drag || state.drag.id !== event.pointerId) return;
      state.drag = null;
      wrap.classList.remove('dragging');
      note(statusText());
    };
    canvas.addEventListener('pointerup', release);
    canvas.addEventListener('pointercancel', release);
    canvas.addEventListener('pointerleave', () => {
      if (state.drag) return;
      state.hoverCandle = null;
      state.hoverMarker = null;
      state.hoverY = NaN;
      $('kline-tooltip').hidden = true;
      draw();
    });
    canvas.addEventListener('dblclick', resetView);
    canvas.addEventListener('wheel', event => { event.preventDefault(); zoom(event); }, { passive: false });
  }

  // panTo follows the pointer: horizontal moves shift time, vertical moves shift
  // price and pin the price axis manually so the drag does not snap back.
  function panTo(at) {
    const drag = state.drag, plot = dims.plot;
    state.view = K.panWindow(drag.view, (drag.x - at.x) / plot.width * (drag.view.end - drag.view.start), chartLimits());
    if (Math.abs(at.y - drag.y) > 2) {
      const shift = (at.y - drag.y) / (dims.priceBottom - plot.top) * (drag.high - drag.low);
      state.yLow = drag.low + shift;
      state.yHigh = drag.high + shift;
      state.yManual = true;
    }
    state.atLatest = atLatest();
    $('kline-new-data').classList.toggle('hidden', !state.newData);
    loadHistory();
    draw();
  }

  // zoom scales time around the cursor time, or the price axis when a modifier is
  // held or the pointer sits over the price labels, like the bubble chart does.
  function zoom(event) {
    if (!scales || !dims || !state.candles.length) return;
    const at = pointer(event), factor = event.deltaY > 0 ? 1.18 : 1 / 1.18;
    if (event.ctrlKey || event.shiftKey || at.x > dims.plot.right) {
      const anchor = scales.priceAt(Math.max(dims.plot.top, Math.min(dims.priceBottom, at.y)));
      const auto = K.priceRange(state.candles, state.view, step()) || [state.yLow, state.yHigh];
      const minimum = Math.max((state.yHigh - state.yLow) * .0002, 1e-9);
      const maximum = Math.max((auto[1] - auto[0]) * 8, minimum);
      const next = Math.max(minimum, Math.min(maximum, (state.yHigh - state.yLow) * factor));
      const ratio = (anchor - state.yLow) / (state.yHigh - state.yLow);
      state.yLow = anchor - next * ratio;
      state.yHigh = state.yLow + next;
      state.yManual = true;
    } else {
      state.view = K.zoomWindow(state.view, scales.timeAt(at.x), factor, chartLimits());
      state.atLatest = atLatest();
      loadHistory();
    }
    $('kline-new-data').classList.toggle('hidden', !state.newData);
    draw();
  }

  function resetView() {
    state.yManual = false;
    state.keepSpan = 0;
    state.view = initialWindow();
    state.atLatest = true;
    state.newData = false;
    $('kline-new-data').classList.add('hidden');
    note(statusText());
    draw();
  }

  function goLatest() {
    const last = newestCandle();
    if (!last) return;
    const span = state.view.end - state.view.start || K.DEFAULT_VISIBLE * step();
    const end = last.t + (K.RIGHT_PAD + 1) * step();
    state.view = K.clampWindow({ start: end - span, end }, chartLimits());
    state.atLatest = true;
    state.newData = false;
    $('kline-new-data').classList.add('hidden');
    note(statusText());
    loadCandles();
  }

  function bindControls() {
    const boxes = [[STORE_ALERTS, 'kline-alerts', 'alerts'], [STORE_SIGNALS, 'kline-signals', 'signals'], [STORE_AGGRESSIVE, 'kline-aggressive', 'aggressive'], [STORE_STEADY, 'kline-steady', 'steady']];
    for (const [key, id, toggle] of boxes) {
      const box = $(id);
      box.checked = state.toggles[toggle];
      box.addEventListener('change', () => {
        state.toggles[toggle] = box.checked;
        remember(key, box.checked);
        note(statusText());
        renderSignals();
        draw();
      });
    }
    $('kline-latest').addEventListener('click', goLatest);
    $('kline-reset').addEventListener('click', resetView);
    $('kline-refresh').addEventListener('click', () => { loadCandles(); loadAlerts(); loadSignals(); });
    $('kline-jump').addEventListener('submit', event => {
      event.preventDefault();
      const target = $('kline-symbol-input').value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 32);
      if (target) location.assign(pageUrl(target, state.interval));
    });
  }

  function boot() {
    buildIntervals();
    bindControls();
    bindCanvas();
    palette();
    resize();
    syncIntervals();
    $('kline-symbol-input').value = state.symbol;
    $('kline-back').href = base + 'alerts';
    if (!state.symbol) {
      $('kline-title').textContent = '币对 K 线';
      showError('缺少币对参数：请从报警页点击币对名，或在上方输入 Binance 永续合约名后跳转。');
      note('等待币对');
      renderSignals();
      draw();
      return;
    }
    $('kline-title').textContent = state.symbol + ' K 线';
    document.title = state.symbol + ' K线 · 清算墙雷达';
    note('正在读取 ' + state.symbol + ' ' + state.interval + ' K 线');
    loadCandles();
    loadAlerts();
    loadSignals();
    setInterval(() => { if (!document.hidden && !state.drag) loadCandles(); }, CANDLE_REFRESH_MS);
    setInterval(() => { if (!document.hidden && !state.drag) { loadAlerts(); loadSignals(); } }, SIGNAL_REFRESH_MS);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) { loadCandles(); loadAlerts(); loadSignals(); } });
    window.addEventListener('resize', resize);
    window.addEventListener('themechange', () => { palette(); draw(); });
    if (window.ResizeObserver) new ResizeObserver(resize).observe($('kline-wrap'));
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
