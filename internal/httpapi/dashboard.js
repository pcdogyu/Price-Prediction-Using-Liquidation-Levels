'use strict';

const NS = 'http://www.w3.org/2000/svg';
const embeddedChart = new URLSearchParams(window.location.search).get('embed') === '1';
document.documentElement.classList.toggle('embedded-chart', embeddedChart);
const paint = value => window.LiquidationTheme.color(value);
const $ = id => document.getElementById(id);
const cache = { signal: null, map: null, market: null, volume: null };
let savedInterval = '15m';
try { savedInterval = localStorage.getItem('liquidation.interval') || savedInterval; } catch (_) { /* browser storage is optional */ }
const defaultLiquidationMinimumUSD = 0;
const defaultLiquidationLongOffset = 20;
const defaultLiquidationShortOffset = 20;
const liquidationOffsetLimits = { minimum: 0, maximum: 200 };
function storedNonNegativeNumber(key, fallback, maximum = Number.POSITIVE_INFINITY) {
  try {
    const value = Number(localStorage.getItem(key));
    return Number.isFinite(value) && value >= 0 ? Math.min(maximum, value) : fallback;
  } catch (_) { return fallback; }
}
const storedLiquidationMinimumUSD = storedNonNegativeNumber('liquidation.minimumUSD', defaultLiquidationMinimumUSD);
const storedLiquidationLongOffset = storedNonNegativeNumber('liquidation.longOffset', defaultLiquidationLongOffset, liquidationOffsetLimits.maximum);
const storedLiquidationShortOffset = storedNonNegativeNumber('liquidation.shortOffset', defaultLiquidationShortOffset, liquidationOffsetLimits.maximum);
const defaultVisibleCandles = 120;
const chartHeightLimits = { minimum: 320, maximum: 1600 };
const storedChartHeight = (() => {
  try {
    const value = Number(localStorage.getItem('liquidation.chartHeight'));
    return Number.isFinite(value) ? Math.max(chartHeightLimits.minimum, Math.min(chartHeightLimits.maximum, Math.round(value))) : null;
  } catch (_) { return null; }
})();
// Wheel zoom is regional: the candle strip changes how many candles it shows, and
// /api/v1/market caps one page at 500 candles, so that is the widest zoom out. The two
// profile panels cannot fetch anything, so their wheel scales the bar lengths instead.
const candleZoomLimits = { minimum: 24, maximum: 500 };
const profileZoomLimits = { minimum: .25, maximum: 12 };
const marketIntervals = ['1m', '2m', '3m', '5m', '15m', '30m', '1h', '4h', '8h', '12h', '24h'];
if (!marketIntervals.includes(savedInterval)) savedInterval = '15m';
const marketBackfillPageLimit = 500;
const marketBackfillRetryDelays = [1000, 3000, 10000];
const chart = {
  interval: savedInterval,
  candles: [], signals: [], liquidations: [], liquidationsTruncated: false, liquidationMinimumUSD: storedLiquidationMinimumUSD, liquidationLongOffset: storedLiquidationLongOffset, liquidationShortOffset: storedLiquidationShortOffset, latestPriceTradeID: 0, latestPriceTick: null, windowEnd: 0, visibleCount: defaultVisibleCandles, volumeZoom: 1, liquidationZoom: 1,
  hasMore: false, nextBefore: '', loadingOlder: false,
  yLow: null, yHigh: null, autoSpan: null, yManual: false,
  manualHeight: storedChartHeight,
  atLatest: true, newData: false, drag: null
};
const tradeQuery = new URLSearchParams(window.location.search);
const requestedTradeSymbolRaw = String(tradeQuery.get('symbol') || '').trim();
const requestedTradeSymbol = window.BubbleChart.normalizeMarketSymbol(requestedTradeSymbolRaw);
const normalizeTradeExchange = value => {
  const exchange = String(value || '').trim().toLowerCase();
  return exchange === 'okx' || exchange === 'binance' || exchange === 'gate' ? exchange : '';
};
const tradeExchangeLabel = value => {
  const exchange = normalizeTradeExchange(value);
  if (exchange === 'binance') return 'Binance';
  if (exchange === 'gate') return 'Gate';
  if (exchange === 'okx') return 'OKX';
  return '交易所';
};
const tradeExchangeSummary = markers => {
  const exchanges = [...new Set((Array.isArray(markers) ? markers : [])
    .map(marker => normalizeTradeExchange(marker && marker.exchange))
    .filter(Boolean))];
  return exchanges.length ? exchanges.map(tradeExchangeLabel).join('/') : '账户';
};
let symbol = 'ETHUSDT';
const initialSymbolButton = document.querySelector('button[data-symbol].active');
if (initialSymbolButton) symbol = initialSymbolButton.dataset.symbol;
if (requestedTradeSymbol) symbol = requestedTradeSymbol;
let symbolCatalog = [];
const tradeView = {
  ref: String(tradeQuery.get('trade_ref') || '').trim(),
  exchange: normalizeTradeExchange(tradeQuery.get('exchange')),
  requestedSymbol: requestedTradeSymbol,
  focusAt: window.BubbleChart.tradeTime(tradeQuery.get('focus_at')),
  markers: [],
  referenceMarkers: [],
  historyRows: [],
  historyLoaded: false,
  historyLoading: false,
  historyError: '',
  enabled: false,
  referenceError: requestedTradeSymbolRaw && !requestedTradeSymbol ? 'URL 中的交易币对格式不受支持' : ''
};
let refreshing = false;
let refreshSequence = 0;
let refreshController;
let olderController;
const marketHistoryCache = new Map();
const automaticMarketBackfill = {
  generation: 0,
  timer: 0,
  symbol: '',
  queue: [],
  current: '',
  delayMs: 350,
  running: false,
  runningGeneration: 0,
  active: false,
  paused: false,
  idleWaiters: []
};
const manualMarketBackfill = { active: false };
const visibleMarketBackfill = { controller: null, sequence: 0 };
const marketRequestTimeoutMs = 15000;
let streamError = '';
let lastFailures = [];
let logCursor = '';
let logEntries = [];
let captureStateHoldUntil = 0;

const api = path => new URL('api/v1/' + path, document.baseURI).toString();
const tvbotAPI = path => new URL('tvbot/' + path, document.baseURI).toString();
const finite = value => typeof value === 'number' && Number.isFinite(value);
const moneyFormat = Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const compactFormat = Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 2 });
const quantityFormat = Intl.NumberFormat('en-US', { maximumFractionDigits: 8 });
const money = value => finite(value) ? moneyFormat.format(value) : '—';
const compact = value => finite(value) ? compactFormat.format(value) : '—';
const pct = value => finite(value) ? (value * 100).toFixed(1) + '%' : '—';
const zoomLabel = value => '×' + (Math.round(value * 100) / 100);
const zoomSuffix = value => value === 1 ? '' : ' ' + zoomLabel(value);
const when = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const intervalName = { '1m': '1分钟', '2m': '2分钟', '3m': '3分钟', '5m': '5分钟', '15m': '15分钟', '30m': '30分钟', '1h': '1小时', '4h': '4小时', '8h': '8小时', '12h': '12小时', '24h': '24小时' };
const intervalMilliseconds = { '1m': 60000, '2m': 120000, '3m': 180000, '5m': 300000, '15m': 900000, '30m': 1800000, '1h': 3600000, '4h': 14400000, '8h': 28800000, '12h': 43200000, '24h': 86400000 };
let bubbleHits = () => [];
let tooltipHideTimer;
let chartTipsBound = false;
let lastBubbleDraw = 0;
const liquidationWallWidthScale = .9;
// Kept for compatibility with the dashboard asset contract tests; rank labels
// now use a fixed left-overlay width instead of scaling with the right axis.
const rankLabelWidthScale = .8;
// Reserve 50% less width for the far-right price axis and its labels.
const rightPriceAxisWidthScale = .50;
const rightPriceAxisShift = (1740 - 1510) * (1 - rightPriceAxisWidthScale);
const liquidationWallRight = 1510 + rightPriceAxisShift;
const liquidationWallWidth = Math.round((1510 - 1315) * liquidationWallWidthScale);
const rightPriceAxisX = x => liquidationWallRight + (x - 1510) * rightPriceAxisWidthScale;
const chartLeft = 72;
const liquidationOverlayLeft = chartLeft + 12;
const liquidationRankLabelWidth = 120;
const liquidationOverlayGap = 8;
const liquidationLeft = liquidationOverlayLeft;
const liquidationRight = liquidationLeft + liquidationWallWidth;
const rankLabelLeft = liquidationRight + liquidationOverlayGap;
const rankLabelRight = rankLabelLeft + liquidationRankLabelWidth;
const liquidationOverlayRight = rankLabelRight;
const rightPriceLabelWidth = Math.round(112 * rightPriceAxisWidthScale);
const volumeToPriceLabelGap = 4;
const candleToVolumeGap = 8;
// Give the candle strip the empty area immediately before the right-side volume
// profile while keeping the volume, liquidation wall, and price axis panels in place.
const candleRight = 1315;
const volumeRight = liquidationWallRight - rightPriceLabelWidth - volumeToPriceLabelGap;
const volumeLeft = candleRight + candleToVolumeGap;
const dims = { height: 560, width: 1740, left: chartLeft, top: 24, bottom: 42, candleRight, volumeLeft, volumeRight, liquidationOverlayLeft, liquidationOverlayRight, liquidationLeft, liquidationRight, profileRight: liquidationWallRight, axisPriceRight: rightPriceAxisX(1584), rankLabelLeft, rankLabelRight, rightPriceLabelWidth };
const embeddedChartCandleRight = 1668;
if (!intervalName[chart.interval]) chart.interval = '15m';

function svgNode(tag, attrs = {}, text = '') {
  const element = document.createElementNS(NS, tag);
  Object.entries(attrs).forEach(([key, value]) => element.setAttribute(key, (key === 'fill' || key === 'stroke') && typeof value === 'string' ? paint(value) : value));
  if (text) element.textContent = text;
  return element;
}

async function json(path, options = {}) {
  const response = await fetch(api(path), { cache: 'no-store', credentials: 'same-origin', ...options });
  if (response.status === 401) {
    location.reload();
    throw new Error('登录已过期');
  }
  let body = null;
  try { body = await response.json(); } catch (_) { /* handled below */ }
  if (!response.ok) throw new Error('HTTP ' + response.status + ' · ' + (body?.detail || response.statusText || '请求失败'));
  return body;
}

async function tvbotJSON(path, options = {}) {
  const response = await fetch(tvbotAPI(path), { cache: 'no-store', credentials: 'same-origin', ...options });
  if (response.status === 401) throw new Error('TVBot历史交易暂不可用（未登录或登录已过期）');
  let body = null;
  try { body = await response.json(); } catch (_) { /* handled below */ }
  if (!response.ok) throw new Error('HTTP ' + response.status + ' · ' + (body?.detail || response.statusText || '请求失败'));
  return body;
}

function tradeMarkerKey(marker) {
  return [
    normalizeTradeExchange(marker && marker.exchange),
    String(marker && marker.symbol || ''),
    String(marker && marker.side || ''),
    String(marker && marker.phase || ''),
    String(marker && marker.time || ''),
    String(marker && marker.price || ''),
    String(marker && marker.order_id || ''),
    String(marker && marker.trade_id || '')
  ].join('|');
}

function mergeTradeMarkers(...groups) {
  const merged = new Map();
  groups.flat().forEach(marker => {
    if (!marker || !Number.isFinite(marker.time) || !Number.isFinite(marker.price)) return;
    const key = tradeMarkerKey(marker);
    const previous = merged.get(key);
    if (!previous) {
      merged.set(key, marker);
      return;
    }
    merged.set(key, Object.assign({}, previous, marker, { selected: previous.selected || marker.selected === true }));
  });
  return Array.from(merged.values()).sort((a, b) => a.time - b.time || String(a.id || '').localeCompare(String(b.id || '')));
}

function refreshTradeMarkers() {
  const historyMarkers = window.BubbleChart.normalizeTradeMarkers(tradeView.historyRows, symbol);
  const referenceMarkers = tradeView.referenceMarkers.filter(marker => marker.symbol === symbol);
  tradeView.markers = mergeTradeMarkers(historyMarkers, referenceMarkers);
  tradeView.enabled = tradeView.historyLoaded || tradeView.markers.length > 0;
}

function tradeViewError() {
  return [tradeView.referenceError, tradeView.historyError].filter(Boolean).join('；');
}

function loadTradeView() {
  if (!tradeView.ref) return;
  const storageKey = 'tvbot.analysisChart.' + tradeView.ref;
  let payload = null;
  try {
    const now = Date.now();
    for (let index = localStorage.length - 1; index >= 0; index--) {
      const key = localStorage.key(index) || '';
      if (!key.startsWith('tvbot.analysisChart.')) continue;
      try {
        const createdAt = Number(JSON.parse(localStorage.getItem(key) || '{}').created_at);
        if (Number.isFinite(createdAt) && (now - createdAt > 10 * 60 * 1000 || createdAt - now > 60 * 1000)) localStorage.removeItem(key);
      } catch (_) { /* an unreadable transfer is harmless and can be replaced */ }
    }
    const raw = localStorage.getItem(storageKey);
    payload = raw ? JSON.parse(raw) : null;
    const createdAt = Number(payload?.created_at);
    if (Number.isFinite(createdAt) && (now - createdAt > 10 * 60 * 1000 || createdAt - now > 60 * 1000)) {
      localStorage.removeItem(storageKey);
      payload = null;
    }
  } catch (_) {
    payload = null;
  }
  if (!payload || !Array.isArray(payload.rows)) {
    tradeView.referenceError = '交易点引用不存在或已过期，请从交易分析表重新打开';
    return;
  }
  const payloadExchange = normalizeTradeExchange(payload.exchange);
  if (tradeView.exchange && payloadExchange && tradeView.exchange !== payloadExchange) {
    tradeView.referenceError = '交易点引用与 URL 交易所不一致';
    return;
  }
  if (!tradeView.exchange && payloadExchange) tradeView.exchange = payloadExchange;
  const scopedRows = payloadExchange
    ? payload.rows.filter(row => !normalizeTradeExchange(row && row.exchange) || normalizeTradeExchange(row.exchange) === payloadExchange)
    : payload.rows;
  const payloadSymbol = window.BubbleChart.normalizeMarketSymbol(payload.symbol);
  if (!payloadSymbol) {
    tradeView.referenceError = '交易引用中的币对无法转换为 Binance USDT K 线币对';
    return;
  }
  if (!tradeView.requestedSymbol && payloadSymbol) {
    symbol = payloadSymbol;
  }
  if (tradeView.requestedSymbol && payloadSymbol && tradeView.requestedSymbol !== payloadSymbol) {
    tradeView.referenceError = '交易点引用与 URL 币对不一致';
    return;
  }
  const focus = window.BubbleChart.tradeTime(payload.focus_at || tradeQuery.get('focus_at'));
  if (Number.isFinite(focus)) tradeView.focusAt = focus;
  const selectedKey = String(payload.selected_key || '');
  tradeView.referenceMarkers = window.BubbleChart.normalizeTradeMarkers(scopedRows, symbol, selectedKey);
  refreshTradeMarkers();
  if (!tradeView.referenceMarkers.length) tradeView.referenceError = '该交易引用没有可定位的有效开平仓价格或时间';
}

loadTradeView();

async function loadHistoricalTradeView() {
  if (embeddedChart || tradeView.historyLoaded || tradeView.historyLoading) return;
  tradeView.historyLoading = true;
  tradeView.historyError = '';
  showFailures(lastFailures);
  try {
    const packet = await tvbotJSON('analysis?price_days=1&pnl_days=30');
    // An account with no closed trades is valid: older TVBot responses and
    // cached payloads may encode the empty list as null. Treat both forms as
    // an empty history so the bubble chart remains usable.
    tradeView.historyRows = Array.isArray(packet?.position_trades) ? packet.position_trades : [];
    tradeView.historyLoaded = true;
    refreshTradeMarkers();
  } catch (error) {
    if (error?.name !== 'AbortError') tradeView.historyError = error.message || '历史交易数据不可用';
  } finally {
    tradeView.historyLoading = false;
    refreshTradeMarkers();
    if (cache.market) draw(cache.market, cache.map, cache.signal, cache.volume);
    showFailures(lastFailures);
  }
}

function catalogSymbol(value) {
  const raw = String(value || '').trim().toUpperCase();
  if (!raw) return '';
  const exact = symbolCatalog.find(item => item.symbol === raw);
  if (exact) return exact.symbol;
  const normalized = window.BubbleChart.normalizeMarketSymbol(raw);
  const normalizedItem = symbolCatalog.find(item => window.BubbleChart.normalizeMarketSymbol(item.symbol) === normalized);
  if (normalizedItem) return normalizedItem.symbol;
  const base = symbolCatalog.find(item => item.base_asset === raw);
  return base ? base.symbol : '';
}

function activateSymbol(next) {
  next = String(next || '').toUpperCase();
  if (!next || next === symbol) {
    const input = $('symbol-search');
    if (input) input.value = symbol;
    return;
  }
  symbol = next;
  document.querySelectorAll('button[data-symbol]').forEach(item => item.classList.toggle('active', item.dataset.symbol === symbol));
  const input = $('symbol-search');
  if (input) input.value = symbol;
  cache.signal = cache.map = cache.market = cache.volume = null;
  refreshTradeMarkers();
  tradeView.focusAt = NaN;
  tradeView.referenceError = '';
  resetChartState({ cancelBackfill: true });
  refresh(true);
}

async function loadSymbolCatalog() {
  const state = $('symbol-catalog-state');
  try {
    const packet = await json('symbols');
    symbolCatalog = Array.isArray(packet?.symbols) ? packet.symbols.filter(item => item?.symbol && item?.base_asset) : [];
    const list = $('symbol-options');
    if (list) {
      list.replaceChildren(...symbolCatalog.map(item => {
        const option = document.createElement('option');
        option.value = item.symbol;
        option.label = item.base_asset;
        return option;
      }));
    }
    if (state) state.textContent = symbolCatalog.length + ' 个可交易 USDT 永续';
    if (tradeView.requestedSymbol && !symbolCatalog.some(item => window.BubbleChart.normalizeMarketSymbol(item.symbol) === symbol)) {
      tradeView.referenceError = 'Binance 不支持 ' + symbol + ' 永续 K 线，交易分析表不受影响';
    }
  } catch (error) {
    if (state) state.textContent = '币对目录暂不可用，仍可使用已配置币对';
  }
}

function marketPath(before = '', options = {}) {
  const interval = options.interval || chart.interval;
  const requestedSymbol = options.symbol || symbol;
  const limit = Number.isFinite(options.limit) ? Math.max(1, Math.min(500, Math.round(options.limit))) : chart.visibleCount;
  const params = new URLSearchParams({ symbol: requestedSymbol, interval, limit: String(limit) });
  if (before) params.set('before', before);
  else if (requestedSymbol === symbol && interval === chart.interval && tradeView.enabled && Number.isFinite(tradeView.focusAt) && !chart.candles.length) {
    const duration = intervalMilliseconds[interval] || 900000;
    params.set('before', new Date(tradeView.focusAt + duration).toISOString());
  }
  if (options.repair) params.set('repair', '1');
  return 'market?' + params.toString();
}

function marketHistoryPath(interval = chart.interval, targetSymbol = symbol) {
  return 'market-history?' + new URLSearchParams({ symbol: targetSymbol, interval }).toString();
}

function visibleMarketPath() {
  const candles = visibleCandles();
  if (!candles.length) return '';
  const last = Date.parse(candles.at(-1).time);
  const before = chart.atLatest || !Number.isFinite(last)
    ? ''
    : new Date(last + (intervalMilliseconds[chart.interval] || 900000)).toISOString();
  const limit = Math.min(500, Math.max(chart.visibleCount, candles.length));
  return marketPath(before, { limit, repair: true });
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

// Calculate a one-time height for the explicit "自动" button. The page does
// not call this during drawing or viewport changes, so the resulting height is
// treated like any other manually applied height until the user adapts again.
function calculateBubbleChartViewportHeight() {
  const bubbleChartDefaultHeight = 874;
  const bubbleChartViewportScale = 1.3;
  const wrap = document.querySelector('.chart-wrap');
  if (!wrap) return null;
  if (window.innerWidth < 700) return Math.round(wrap.getBoundingClientRect().height);
  const viewportHeight = window.visualViewport?.height || window.innerHeight;
  const available = Math.floor(viewportHeight - wrap.getBoundingClientRect().top - 12);
  return Math.max(chartHeightLimits.minimum, Math.min(bubbleChartDefaultHeight, Math.round(available * bubbleChartViewportScale)));
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

function mergeLiquidations(current, incoming, targetSymbol = symbol) {
  return window.BubbleChart.mergeEvents(current, incoming, targetSymbol);
}

function marketHistoryKey(targetSymbol = symbol, interval = chart.interval) {
  return targetSymbol + '|' + interval;
}

function marketHistoryState(targetSymbol = symbol, interval = chart.interval) {
  const key = marketHistoryKey(targetSymbol, interval);
  let state = marketHistoryCache.get(key);
  if (!state) {
    state = {
      symbol: targetSymbol,
      interval,
      initialized: false,
      complete: false,
      loading: false,
      controller: null,
      candles: [],
      signals: [],
      liquidations: [],
      liquidationsTruncated: false,
      hasMore: false,
      nextBefore: '',
      pages: 0,
      retries: 0,
      retryAt: 0,
      failed: false,
      error: '',
      remoteSyncing: false,
      remoteSyncError: '',
      localThrough: '',
      remoteSyncedAt: '',
      cursorHistory: new Set(),
      snapshot: false,
      view: null
    };
    marketHistoryCache.set(key, state);
  }
  return state;
}

function updateMarketHistoryState(state, view, reset = false, targetSymbol = state.symbol) {
  const incomingCandles = Array.isArray(view?.candles) ? view.candles : [];
  const incomingSignals = Array.isArray(view?.model_signals) ? view.model_signals : [];
  const incomingLiquidations = Array.isArray(view?.liquidations) ? view.liquidations : [];
  state.candles = reset ? incomingCandles.slice() : mergeCandles(state.candles, incomingCandles);
  state.signals = reset ? incomingSignals.slice() : mergeSignals(state.signals, incomingSignals);
  state.liquidations = reset ? mergeLiquidations([], incomingLiquidations, targetSymbol) : mergeLiquidations(state.liquidations, incomingLiquidations, targetSymbol);
  state.liquidationsTruncated = reset ? !!view?.liquidations_truncated : state.liquidationsTruncated || !!view?.liquidations_truncated;
  state.initialized = true;
  state.remoteSyncing = !!view?.remote_syncing;
  state.remoteSyncError = view?.remote_sync_error || '';
  state.localThrough = view?.local_through || '';
  state.remoteSyncedAt = view?.remote_synced_at || '';
  state.complete = (!view?.has_more || !view?.next_before) && !state.remoteSyncing && !state.remoteSyncError;
  state.hasMore = !!view?.has_more;
  state.nextBefore = view?.next_before || '';
  state.pages += 1;
  state.error = '';
  state.retryAt = 0;
  state.failed = false;
  state.snapshot = reset ? view?.snapshot === true : state.snapshot || view?.snapshot === true;
  if (reset) state.cursorHistory.clear();
  state.view = Object.assign({}, view, {
    symbol: targetSymbol,
    interval: state.interval,
    candles: state.candles,
    model_signals: state.signals,
    liquidations: state.liquidations,
    liquidations_truncated: state.liquidationsTruncated,
    has_more: state.hasMore,
    next_before: state.nextBefore
  });
  return state;
}

function syncActiveMarketHistory(view, older, reset) {
  const state = marketHistoryState(symbol, chart.interval);
  state.candles = chart.candles;
  state.signals = chart.signals;
  state.liquidations = chart.liquidations;
  state.liquidationsTruncated = chart.liquidationsTruncated;
  state.initialized = true;
  state.remoteSyncing = !!view?.remote_syncing;
  state.remoteSyncError = view?.remote_sync_error || '';
  state.localThrough = view?.local_through || '';
  state.remoteSyncedAt = view?.remote_synced_at || '';
  if (older || reset) {
    state.complete = (!view?.has_more || !view?.next_before) && !state.remoteSyncing && !state.remoteSyncError;
    state.hasMore = !!view?.has_more;
    state.nextBefore = view?.next_before || '';
    state.pages = reset ? 1 : state.pages + 1;
  }
  state.pages = Math.max(state.pages, 1);
  state.error = '';
  state.failed = false;
  if (reset) state.snapshot = false;
  if (reset) state.cursorHistory.clear();
  state.view = Object.assign({}, view, {
    symbol,
    interval: chart.interval,
    candles: state.candles,
    model_signals: state.signals,
    liquidations: state.liquidations,
    liquidations_truncated: state.liquidationsTruncated,
    has_more: state.hasMore,
    next_before: state.nextBefore
  });
  cache.market = state.view;
  return state;
}

const bubbleLoader = new window.BubbleChart.WindowLoader(json, rows => {
  chart.liquidations = mergeLiquidations(chart.liquidations, rows);
}, state => {
  // Avoid rebuilding thousands of circles for every pagination response.
  if (state.status === 'loading' && Date.now() - lastBubbleDraw < 500 && $('chart-context').textContent.includes('补齐中')) {
    $('chart-context').textContent = $('chart-context').textContent.replace(/ · (补齐中.*|窗口已补齐|等待数据)$/, '') + ' · 补齐中 · ' + state.pages + '页 / ' + state.loaded + '笔';
    return;
  }
  lastBubbleDraw = Date.now();
  draw(cache.market, cache.map, cache.signal, cache.volume);
});

function ensureWindowLiquidations(retry = false) {
  const scale = chartTimeScale(visibleCandles(), dims.left, dims.candleRight);
  if (scale) bubbleLoader.ensure(symbol, scale.start, scale.end, retry, chart.liquidationMinimumUSD);
}

function mergeMarket(view, older, reset) {
  const incomingCandles = view.candles || [];
  const incomingSignals = view.model_signals || [];
  const incomingLiquidations = view.liquidations || [];
  if (reset) {
    chart.candles = incomingCandles.slice();
    chart.signals = incomingSignals.slice();
    chart.liquidations = mergeLiquidations(chart.liquidations, incomingLiquidations);
    chart.liquidationsTruncated = !!view.liquidations_truncated;
    chart.windowEnd = chart.candles.length;
    chart.atLatest = true;
    chart.newData = false;
    chart.yManual = false;
    chart.yLow = chart.yHigh = chart.autoSpan = null;
    if (tradeView.enabled && Number.isFinite(tradeView.focusAt)) chart.atLatest = false;
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
  syncActiveMarketHistory(view, older, reset);
  if (incomingCandles.length) {
    const start = Date.parse(incomingCandles[0].time);
    const end = Math.min(Date.parse(incomingCandles[incomingCandles.length - 1].time) + intervalMilliseconds[chart.interval], view.queryCutoff || Date.now());
    // A capped snapshot still contains every event newer than its oldest timestamp.
    // Leave that timestamp itself uncovered because the cap may split a timestamp tie.
    const coveredStart = view.liquidations_truncated ? Math.min(...incomingLiquidations.map(item => Date.parse(item.event_time))) + 1 : start;
    if (Number.isFinite(coveredStart)) bubbleLoader.cover(coveredStart, end);
  }
  const realtimePrice = view.realtime_price;
  if (finite(realtimePrice?.trade_id) && realtimePrice.trade_id >= chart.latestPriceTradeID) {
    chart.latestPriceTradeID = realtimePrice.trade_id;
    chart.latestPriceTick = realtimePrice;
  }
  if (chart.latestPriceTick) applyPriceTick(chart.latestPriceTick, false, true);
  $('new-data').classList.toggle('hidden', !chart.newData);
}

function cancelAutomaticMarketBackfill(clearCache = false) {
  automaticMarketBackfill.generation++;
  clearTimeout(automaticMarketBackfill.timer);
  automaticMarketBackfill.timer = 0;
  automaticMarketBackfill.active = false;
  automaticMarketBackfill.paused = false;
  automaticMarketBackfill.symbol = '';
  automaticMarketBackfill.queue = [];
  automaticMarketBackfill.current = '';
  automaticMarketBackfill.running = false;
  automaticMarketBackfill.runningGeneration = 0;
  marketHistoryCache.forEach(state => {
    state.controller?.abort();
    state.controller = null;
    state.loading = false;
  });
  notifyAutomaticMarketBackfillIdle();
  if (clearCache) marketHistoryCache.clear();
  updateMarketBackfillButton();
}

function waitForAutomaticMarketBackfillIdle() {
  if (!automaticMarketBackfill.running) return Promise.resolve();
  return new Promise(resolve => automaticMarketBackfill.idleWaiters.push(resolve));
}

function notifyAutomaticMarketBackfillIdle() {
  const waiters = automaticMarketBackfill.idleWaiters.splice(0);
  waiters.forEach(resolve => resolve());
}

async function pauseAutomaticMarketBackfillForManual() {
  const shouldResume = automaticMarketBackfill.active && automaticMarketBackfill.symbol === symbol;
  if (!shouldResume) return false;
  const idle = waitForAutomaticMarketBackfillIdle();
  automaticMarketBackfill.paused = true;
  clearTimeout(automaticMarketBackfill.timer);
  automaticMarketBackfill.timer = 0;
  const currentState = automaticMarketBackfill.current
    ? marketHistoryState(symbol, automaticMarketBackfill.current)
    : null;
  currentState?.controller?.abort();
  updateMarketBackfillButton();
  await idle;
  return shouldResume;
}

function resumeAutomaticMarketBackfill() {
  automaticMarketBackfill.paused = false;
  if (!automaticMarketBackfill.active || automaticMarketBackfill.symbol !== symbol) {
    updateMarketBackfillButton();
    return;
  }
  updateMarketBackfillButton();
  if (!automaticMarketBackfill.timer && !automaticMarketBackfill.running) {
    runAutomaticMarketBackfill(automaticMarketBackfill.generation);
  }
}

function marketBackfillProgress() {
  const jobs = marketIntervals.map(interval => marketHistoryState(symbol, interval));
  const completed = jobs.filter(state => state.complete).length;
  const current = automaticMarketBackfill.current || chart.interval;
  const currentState = marketHistoryState(symbol, current);
  const pages = currentState.loading
    ? ' · 服务端快照读取中…'
    : currentState.remoteSyncing
      ? ' · 本地快照 · Binance后台同步中'
    : currentState.remoteSyncError
      ? ' · 本地快照 · Binance同步失败，等待重试'
    : currentState.snapshot
      ? ' · 服务端快照 ' + currentState.candles.length + '根'
      : currentState.pages ? ' · ' + currentState.pages + '页' : '';
  return { completed, total: jobs.length, current, pages };
}

function automaticMarketBackfillLabel() {
  if (!automaticMarketBackfill.active) return '';
  const progress = marketBackfillProgress();
  return ' · 多周期K线回补 ' + progress.current + progress.pages + ' · ' + progress.completed + '/' + progress.total + '周期';
}

function moveMarketBackfillToFront(interval) {
  if (!interval || !marketIntervals.includes(interval)) return;
  automaticMarketBackfill.queue = [interval, ...automaticMarketBackfill.queue.filter(item => item !== interval)];
}

function nextMarketBackfillInterval() {
  const now = Date.now();
  for (const interval of automaticMarketBackfill.queue) {
    const state = marketHistoryState(symbol, interval);
    if (state.loading || state.complete || state.failed) continue;
    if (state.retryAt > now) continue;
    return interval;
  }
  return '';
}

function finishAutomaticMarketBackfill() {
  automaticMarketBackfill.active = false;
  automaticMarketBackfill.paused = false;
  automaticMarketBackfill.timer = 0;
  automaticMarketBackfill.current = '';
  updateMarketBackfillButton();
  if (cache.market) draw(cache.market, cache.map, cache.signal, cache.volume);
}

function scheduleAutomaticMarketBackfillRetry(generation, delay) {
  clearTimeout(automaticMarketBackfill.timer);
  if (automaticMarketBackfill.paused || !automaticMarketBackfill.active) {
    automaticMarketBackfill.timer = 0;
    return;
  }
  automaticMarketBackfill.timer = setTimeout(() => runAutomaticMarketBackfill(generation), Math.max(0, delay));
}

async function runAutomaticMarketBackfillOnce(generation) {
  if (generation !== automaticMarketBackfill.generation || !automaticMarketBackfill.active || automaticMarketBackfill.symbol !== symbol || automaticMarketBackfill.paused) return;
  if (manualMarketBackfill.active || visibleMarketBackfill.controller) {
    scheduleAutomaticMarketBackfillRetry(generation, automaticMarketBackfill.delayMs);
    return;
  }
  const interval = nextMarketBackfillInterval();
  if (!interval) {
    const retryAt = marketIntervals.map(item => marketHistoryState(symbol, item).retryAt).filter(value => value > Date.now()).sort((a, b) => a - b)[0];
    if (retryAt) {
      scheduleAutomaticMarketBackfillRetry(generation, retryAt - Date.now());
      return;
    }
    finishAutomaticMarketBackfill();
    return;
  }
  automaticMarketBackfill.current = interval;
  updateMarketBackfillButton();
  let loaded = await loadMarketHistorySnapshot({ interval, background: true });
  if (!loaded) loaded = await loadOlder({ interval, limit: marketBackfillPageLimit, background: true });
  if (generation !== automaticMarketBackfill.generation || !automaticMarketBackfill.active || automaticMarketBackfill.symbol !== symbol || automaticMarketBackfill.paused) return;
  const state = marketHistoryState(symbol, interval);
  if (loaded) {
    state.retries = 0;
    state.retryAt = 0;
    scheduleAutomaticMarketBackfillRetry(generation, state.remoteSyncing ? 2500 : automaticMarketBackfill.delayMs);
    return;
  }
  state.retries += 1;
  if (state.retries <= marketBackfillRetryDelays.length) {
    state.retryAt = Date.now() + marketBackfillRetryDelays[state.retries - 1];
    scheduleAutomaticMarketBackfillRetry(generation, marketBackfillRetryDelays[state.retries - 1]);
    return;
  }
  state.failed = true;
  state.retryAt = 0;
  state.error = state.error || '连续回补失败，等待页面恢复后重试';
  scheduleAutomaticMarketBackfillRetry(generation, automaticMarketBackfill.delayMs);
}

async function runAutomaticMarketBackfill(generation) {
  if (automaticMarketBackfill.running || automaticMarketBackfill.paused) return;
  automaticMarketBackfill.running = true;
  automaticMarketBackfill.runningGeneration = generation;
  try {
    await runAutomaticMarketBackfillOnce(generation);
  } finally {
    if (automaticMarketBackfill.runningGeneration === generation) {
      automaticMarketBackfill.running = false;
      automaticMarketBackfill.runningGeneration = 0;
      notifyAutomaticMarketBackfillIdle();
    }
  }
}

function scheduleAutomaticMarketBackfill(preferredInterval = chart.interval) {
  if (embeddedChart) return;
  if (automaticMarketBackfill.symbol && automaticMarketBackfill.symbol !== symbol) cancelAutomaticMarketBackfill(true);
  if (!automaticMarketBackfill.symbol) automaticMarketBackfill.symbol = symbol;
  marketIntervals.forEach(interval => {
    const state = marketHistoryState(symbol, interval);
    if (state.failed) {
      state.failed = false;
      state.retries = 0;
      state.retryAt = 0;
      state.error = '';
    }
  });
  if (manualMarketBackfill.active || automaticMarketBackfill.paused) {
    updateMarketBackfillButton();
    return;
  }
  if (!automaticMarketBackfill.active) {
    automaticMarketBackfill.active = true;
    automaticMarketBackfill.queue = marketIntervals.slice();
    automaticMarketBackfill.generation++;
  }
  moveMarketBackfillToFront(preferredInterval);
  updateMarketBackfillButton();
  if (!automaticMarketBackfill.timer) runAutomaticMarketBackfill(automaticMarketBackfill.generation);
}

function updateMarketBackfillButton() {
  const button = $('market-backfill');
  if (!button) return;
  const hasLoadedMarket = chart.candles.length > 0;
  const busy = manualMarketBackfill.active || chart.loadingOlder;
  button.disabled = busy || !hasLoadedMarket;
  button.textContent = manualMarketBackfill.active
    ? '回补中…'
    : '回补K线+VAL/VAH';
  button.setAttribute('aria-busy', String(busy));
  button.setAttribute('aria-label', manualMarketBackfill.active
    ? '正在回补当前显示的K线、成交量分布、VAL和VAH'
    : automaticMarketBackfill.active
      ? '后台多周期回补进行中，点击优先回补当前显示的K线、成交量分布、VAL和VAH'
      : '回补当前显示的K线、成交量分布、VAL和VAH');
  const state = $('market-backfill-state');
  if (state) {
    const progress = marketBackfillProgress();
    state.textContent = manualMarketBackfill.active
      ? '手动回补当前 ' + chart.interval + ' · 后台队列已暂停'
      : automaticMarketBackfill.active
      ? '后台回补：' + progress.current + progress.pages + ' · 已完成 ' + progress.completed + '/' + progress.total + ' 个周期 · 手动回补可用'
      : progress.completed === progress.total && progress.total > 0
        ? '后台回补完成：' + progress.total + ' 个周期'
        : '';
  }
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
  const candles = chart.candles.slice(Math.max(0, end - chart.visibleCount), end);
  const last = Date.parse(candles[candles.length - 1]?.time);
  const start = last + intervalMilliseconds[chart.interval] * (1 - chart.visibleCount);
  return candles.filter(candle => Date.parse(candle.time) >= start);
}

function currentRange(candles) {
  const automatic = window.BubbleChart.candleRange(candles);
  if (!automatic) return null;
  if (chart.yManual && finite(chart.yLow) && finite(chart.yHigh) && chart.yHigh > chart.yLow) return [chart.yLow, chart.yHigh];
  const [low, high] = automatic;
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

function renderLiquidationTop3(map, range) {
  const container = $('liquidation-top3');
  container.replaceChildren();
  if (map?.data_source !== 'coinglass_binance_liqmap') {
    container.appendChild(Object.assign(document.createElement('span'), { textContent: 'CoinGlass 清算数据不可用' }));
    return;
  }
  [['long', '多头清算 Top3', map.top_long_liquidations || []], ['short', '空头清算 Top3', map.top_short_liquidations || []]].forEach(([className, label, peaks]) => {
    const span = document.createElement('span');
    span.className = className;
    span.textContent = label + '：' + (peaks.length ? peaks.map((peak, index) => (index + 1) + ') ' + money(peak.price) + (range && peak.price > range[1] ? ' ↑视野外' : range && peak.price < range[0] ? ' ↓视野外' : '')).join(' / ') : '—');
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

function drawLiquidationRankLabels(svg, map, low, high, y, top, plotBottom, layout) {
  if (map?.data_source !== 'coinglass_binance_liqmap') return;
  const { liquidationRight, rankLabelLeft, rankLabelRight } = layout;
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
    const group = svgNode('g', { 'data-tip': tip, 'data-liquidation-rank': item.prefix + item.rank });
    const connectorX = liquidationRight - 4;
    group.appendChild(svgNode('circle', { cx: connectorX, cy: item.targetY, r: 2.2, fill: item.color }));
    group.appendChild(svgNode('path', { d: 'M ' + connectorX + ' ' + item.targetY + ' L ' + (rankLabelLeft - 8) + ' ' + item.targetY + ' L ' + (rankLabelLeft - 2) + ' ' + item.labelY, fill: 'none', stroke: item.color, 'stroke-width': 1.15, opacity: .9 }));
    group.appendChild(svgNode('rect', { x: rankLabelLeft, y: item.labelY - 9, width: rankLabelRight - rankLabelLeft, height: 18, fill: '#071019', stroke: item.color, 'stroke-width': 1, rx: 4 }));
    group.appendChild(svgNode('text', { x: rankLabelLeft + 6, y: item.labelY + 3.5, fill: item.color, 'font-size': 10.5, 'font-weight': 750 }, text));
    svg.appendChild(group);
  });
}

function liquidationRadius(notionalUSD) {
  return window.BubbleChart.radius(notionalUSD);
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
  const hour = 60 * 60 * 1000;
  const halfHour = 30 * 60 * 1000;
  const span = scale.end - scale.start;
  const width = right - left;
  const labelStep = timeGridStep(span, width, 90, [hour, 2 * hour, 3 * hour, 4 * hour, 6 * hour, 8 * hour, 12 * hour, 24 * hour, 48 * hour, 72 * hour, 7 * 24 * hour, 14 * 24 * hour, 30 * 24 * hour]);
  const majorTicks = window.BubbleChart.timeGridTicks(scale.start, scale.end, halfHour);
  for (const at of majorTicks) {
    const x = scale.x(at);
    const onHour = at % hour === 0;
    svg.appendChild(svgNode('line', { x1: x, y1: top, x2: x, y2: plotBottom, stroke: onHour ? '#315064' : '#234252', 'stroke-width': onHour ? 1 : .8, opacity: onHour ? .92 : .72, 'data-time-grid': 'half-hour', 'data-time-grid-kind': onHour ? 'hour' : 'half-hour' }));
    if (onHour && at % labelStep === 0) {
      const label = new Date(at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
      svg.appendChild(svgNode('text', { x, y: labelY, 'text-anchor': 'middle', fill: '#7895a7', 'font-size': 11 }, label));
    }
  }
}

function bubbleTip(event) {
  const long = event.position_side === 'long';
  const offset = long ? chart.liquidationLongOffset : chart.liquidationShortOffset;
  return 'Binance ' + (long ? '多单爆仓' : '空单爆仓') + '\n时间 ' + when(event.event_time) + '\n成交价 ' + money(event.price) + ' · ' + (long ? '向下' : '向上') + '偏移' + offset + '像素\n数量 ' + quantityFormat.format(event.quantity || 0) + ' · 金额 $' + compact(event.notional_usd);
}

function drawLiquidationBubbles(plot, events, range, scale, y, left, candleRight, top, plotBottom) {
  const pixelScale = dims.height / $('market-chart').getBoundingClientRect().height;
  const offsets = { long: chart.liquidationLongOffset * pixelScale, short: chart.liquidationShortOffset * pixelScale };
  const result = window.BubbleChart.placements(events, range, { left, right: candleRight, top, bottom: plotBottom }, scale.x, y, offsets);
  bubbleHits = window.BubbleChart.hitIndex(result.points);
  result.points.forEach(point => {
    const color = point.event.position_side === 'long' ? '#fb7185' : '#2dd4bf';
    plot.appendChild(svgNode('circle', { cx: point.x, cy: point.y, r: liquidationRadius(point.event.notional_usd), fill: color, 'fill-opacity': .38, stroke: color, 'stroke-width': 1.5, 'data-event-id': point.event.id, 'data-event-price': point.event.price }));
  });
  return result;
}

function tradeMarkerTip(marker) {
  const operation = String(marker.operation || (marker.phase === 'entry' ? 'open' : 'close')).toLowerCase();
  const operationLabel = operation === 'take_profit' ? '止盈' : operation === 'stop_loss' ? '止损' : operation === 'close' ? '平仓' : '开仓';
  const source = String(marker.operation_source || '').toLowerCase();
  const sourceLabel = source === 'manual' ? '手动' : source === 'auto' ? '自动' : source === 'exchange' ? '成交' : '';
  const action = marker.action === 'buy' ? '买入' : '卖出';
  const side = marker.side === 'long' ? '多单' : '空单';
  const details = [];
  if (marker.order_id) details.push('订单号 ' + marker.order_id);
  if (marker.trade_id) details.push('成交号 ' + marker.trade_id);
  if (marker.quantity) details.push('成交量 ' + marker.quantity);
  if (marker.notional_usdt) details.push('名义USDT金额 ' + marker.notional_usdt);
  if (marker.margin_usdt) details.push('保证金USDT金额 ' + marker.margin_usdt);
  if (marker.collapsed_count > 1) details.push('同K线合并 ' + marker.collapsed_count + '个点位');
  if (marker.fill_pnl) details.push('成交盈亏 ' + marker.fill_pnl);
  if (marker.net_pnl) details.push('净盈亏 ' + marker.net_pnl);
  if (marker.fee) details.push('手续费 ' + marker.fee + (marker.fee_ccy ? ' ' + marker.fee_ccy : ''));
  return tradeExchangeLabel(marker.exchange || tradeView.exchange) + ' 下单信息 · ' + (sourceLabel ? sourceLabel + ' ' : '') + operationLabel + ' ' + action + '\n时间 ' + when(marker.time_iso) + '\n价格 ' + money(marker.price) + '\n方向 ' + side + (details.length ? '\n' + details.join('\n') : '') + (marker.selected ? '\n当前点击记录' : '');
}

function drawTradeMarkers(plot, markers, range, scale, y, left, candleRight, top, plotBottom) {
  const interval = intervalMilliseconds[chart.interval] || 900000;
  const collapsed = window.BubbleChart.collapseTradeMarkers(markers, interval);
  const layout = window.BubbleChart.tradeMarkerPlacements(collapsed, range, scale, y, { left, right: candleRight, top, bottom: plotBottom });
  layout.markers.forEach(({ marker, x, priceY, labelX, labelCenterY, labelBelow, labelText, labelWidth }) => {
    const long = marker.side === 'long';
    const closing = marker.phase === 'exit';
    const color = closing ? '#f5c400' : (long ? '#2dd4bf' : '#fb7185');
    const group = svgNode('g', { 'data-trade-marker': marker.id, 'data-tip': tradeMarkerTip(marker) });
    if (marker.selected) group.appendChild(svgNode('rect', { x: labelX - labelWidth / 2 - 3, y: labelCenterY - 12, width: labelWidth + 6, height: 24, fill: 'none', stroke: '#f8fafc', 'stroke-width': 2, rx: 5, opacity: .95 }));
    group.appendChild(svgNode('line', { x1: x, y1: priceY, x2: labelX, y2: labelBelow ? labelCenterY - 10 : labelCenterY + 10, stroke: color, 'stroke-width': 1.2, opacity: .9 }));
    group.appendChild(svgNode('rect', { x: labelX - labelWidth / 2, y: labelCenterY - 10, width: labelWidth, height: 20, fill: color, stroke: '#07141d', 'stroke-width': 1.2, rx: 5 }));
    group.appendChild(svgNode('text', { x: labelX, y: labelCenterY + 4, 'text-anchor': 'middle', fill: '#07141d', 'font-size': 10, 'font-weight': 750 }, labelText));
    plot.appendChild(group);
  });
  return { visible: layout.visible, outside: layout.outside };
}

function drawLiquidationBiasArrow(svg, map, currentPrice, y, low, high, layout) {
  if (map?.data_source !== 'coinglass_binance_liqmap' || !finite(currentPrice) || currentPrice < low || currentPrice > high) return;
  const aboveUSD = Number(map.liquidation_above_usd);
  const belowUSD = Number(map.liquidation_below_usd);
  if (!Number.isFinite(aboveUSD) || !Number.isFinite(belowUSD) || map.liquidation_direction === 'balanced') return;
  const upward = map.liquidation_direction === 'up';
  if (!upward && map.liquidation_direction !== 'down') return;
  const color = upward ? '#22c55e' : '#ef4444';
  const currentY = y(currentPrice);
  const x = layout.liquidationLeft + 25;
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
  const layout = embeddedChart ? { ...dims, candleRight: embeddedChartCandleRight, profileRight: embeddedChartCandleRight, axisPriceRight: 1718 } : dims;
  const bins = embeddedChart ? [] : (map?.bins || []);
  $('chart-title').textContent = (intervalName[chart.interval] || chart.interval) + ' K线 · 成交量分布 · 清算墙';
  const sessionRange = volume?.session_start ? ' (' + when(volume.session_start) + ' → ' + when(volume.session_end) + ')' : '';
  const profileState = volume?.state === 'ok' ? '当前时段成交量' + sessionRange : volume?.state === 'backfilling' ? '成交量回填 ' + ((volume.backfill_progress || 0) * 100).toFixed(0) + '%' + sessionRange : volume?.state === 'stale' ? '成交量已过期' + sessionRange : '成交量不可用';
  const leverageText = (map?.leverages || []).map(value => value + '×').join('/');
  const coinGlassMap = map?.data_source === 'coinglass_binance_liqmap';
  const liquidationName = coinGlassMap ? 'CoinGlass Binance 清算墙' : 'CoinGlass 清算墙不可用';
  const range = currentRange(candles);
  const candleCountText = candles.length < chart.visibleCount ? 'K线 ' + candles.length + '/' + chart.visibleCount + '根' : 'K线 ' + chart.visibleCount + '根';
  const marketSourceText = view?.remote_syncing ? '本地K线 · Binance后台同步中' : view?.remote_sync_error ? '本地K线 · Binance同步失败，等待重试' : String(view?.source || '').includes('local') ? '本地K线' : '';
  if (!embeddedChart) renderLiquidationTop3(map, range);
  $('trade-marker-legend').classList.toggle('hidden', !tradeView.enabled);
  bubbleHits = () => [];
  const state = bubbleLoader.state;
  $('bubble-retry').classList.toggle('hidden', state.status !== 'error');
  const loadState = state.status === 'loading' ? '补齐中 · ' + state.pages + '页 / ' + state.loaded + '笔' : state.status === 'error' ? '补齐失败：' + state.error + '（可重试）' : state.status === 'complete' ? '窗口已补齐' : '等待数据';
  const context = embeddedChart
    ? (chart.atLatest ? '最新K线' : '历史K线')
    : (chart.atLatest ? '最新K线' : '历史K线') + ' · ' + profileState + ' / ' + liquidationName + (marketSourceText ? ' · ' + marketSourceText : '') + ' · K线基准纵轴' + (leverageText ? ' · 杠杆 ' + leverageText : '') + (coinGlassMap && map.captured_at ? ' · 抓取 ' + when(map.captured_at) : '') + ' · ' + candleCountText + ' · 成交量缩放 ' + zoomLabel(chart.volumeZoom) + ' · 清算墙缩放 ' + zoomLabel(chart.liquidationZoom) + automaticMarketBackfillLabel();
  if (!range || !chartTimeScale(candles, layout.left, layout.candleRight)) {
    ['price-low', 'price-high', 'window-start', 'window-end', 'bubble-count', 'trade-marker-count'].forEach(key => svg.removeAttribute('data-' + key));
    $('chart-context').textContent = context + ' · 等待有效K线';
    svg.appendChild(svgNode('text', { x: 800, y: 280, 'text-anchor': 'middle', fill: '#83a0b2' }, '等待有效 K 线数据'));
    return;
  }
  const { height, left, top, bottom, candleRight, volumeLeft, volumeRight, liquidationOverlayLeft, liquidationOverlayRight, liquidationLeft, liquidationRight, profileRight, axisPriceRight, rightPriceLabelWidth } = layout;
  const [low, high] = range;
  svg.setAttribute('data-price-low', low);
  svg.setAttribute('data-price-high', high);
  svg.setAttribute('data-visible-count', chart.visibleCount);
  svg.setAttribute('data-volume-zoom', chart.volumeZoom);
  svg.setAttribute('data-liquidation-zoom', chart.liquidationZoom);
  const plotBottom = height - bottom;
  const y = price => top + (high - price) / (high - low) * (plotBottom - top);
  const defs = svgNode('defs');
  const clip = svgNode('clipPath', { id: 'plot-clip' });
  clip.appendChild(svgNode('rect', { x: left, y: top, width: profileRight - left, height: plotBottom - top }));
  defs.appendChild(clip);
  // Each profile panel keeps its own clip so a wheel-zoomed bar is cut off inside its own
  // column instead of running into the neighbouring panel or the price axis.
  [['volume-clip', volumeLeft, volumeRight], ['liquidation-overlay-clip', liquidationOverlayLeft, liquidationOverlayRight], ['liquidation-clip', liquidationLeft, liquidationRight]].forEach(([id, from, until]) => {
    const panel = svgNode('clipPath', { id });
    panel.appendChild(svgNode('rect', { x: from, y: top, width: Math.max(1, until - from), height: plotBottom - top }));
    defs.appendChild(panel);
  });
  svg.appendChild(defs);
  for (let i = 0; i < 6; i++) {
    const price = high - (high - low) * i / 5;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: '#18313f', 'stroke-width': 1 }));
    svg.appendChild(svgNode('text', { x: left - 8, y: yy + 4, 'text-anchor': 'end', fill: '#7895a7', 'font-size': 11 }, money(price)));
    svg.appendChild(svgNode('line', { x1: profileRight, y1: yy, x2: profileRight + 6, y2: yy, stroke: '#527083', 'stroke-width': 1 }));
    svg.appendChild(svgNode('text', { x: axisPriceRight, y: yy + 4, 'text-anchor': 'end', fill: '#9ab4c4', 'font-size': 11 }, money(price)));
  }
  svg.appendChild(svgNode('line', { x1: volumeLeft - candleToVolumeGap, y1: top, x2: volumeLeft - candleToVolumeGap, y2: plotBottom, stroke: '#315064', 'stroke-width': 1 }));
  svg.appendChild(svgNode('line', { x1: liquidationOverlayLeft - 4, y1: top, x2: liquidationOverlayLeft - 4, y2: plotBottom, stroke: '#315064', 'stroke-width': 1 }));
  svg.appendChild(svgNode('line', { x1: profileRight, y1: top, x2: profileRight, y2: plotBottom, stroke: '#527083', 'stroke-width': 1 }));
  const timeScale = chartTimeScale(candles, left, candleRight);
  svg.setAttribute('data-window-start', new Date(timeScale.start).toISOString());
  svg.setAttribute('data-window-end', new Date(timeScale.end).toISOString());
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
    candles.forEach(candle => {
      const candleStart = new Date(candle.time).getTime();
      const x = timeScale.x(candleStart + timeScale.duration / 2);
      const color = candle.close >= candle.open ? '#2dd4bf' : '#fb7185';
      const bodyTop = y(Math.max(candle.open, candle.close));
      const bodyBottom = y(Math.min(candle.open, candle.close));
      const candleTip = embeddedChart
        ? when(candle.time) + '\nO ' + money(candle.open) + '  H ' + money(candle.high) + '\nL ' + money(candle.low) + '  C ' + money(candle.close)
        : when(candle.time) + '\nO ' + money(candle.open) + '  H ' + money(candle.high) + '\nL ' + money(candle.low) + '  C ' + money(candle.close) + '\n成交额 $' + compact(candle.volume_usd) + (candle.complete ? '' : '\n当前K线尚未完成');
      const group = svgNode('g', { 'data-tip': candleTip });
      group.appendChild(svgNode('line', { x1: x, y1: y(candle.high), x2: x, y2: y(candle.low), stroke: color, 'stroke-width': 1.15 }));
      group.appendChild(svgNode('rect', { x: x - bodyWidth / 2, y: bodyTop, width: bodyWidth, height: Math.max(1.5, bodyBottom - bodyTop), fill: color, rx: .8 }));
      // Keep candle.patterns in the API payload for compatibility, but do not render
      // pattern annotations on the bubbles page.
      const signals = embeddedChart ? [] : (modelSignals.get(new Date(candle.time).getTime()) || []);
      signals.forEach(item => {
        const cy = item.side === 'long' ? y(candle.low) + 10 : y(candle.high) - 10;
        const signalColor = item.side === 'long' ? '#2dd4bf' : '#fb7185';
        group.appendChild(svgNode('path', { d: arrowPath(x, cy, item.side, 5), fill: signalColor, stroke: '#07141d', 'stroke-width': 1, 'data-tip': '模型' + (item.side === 'long' ? '多单' : '空单') + '信号 · ' + pct(item.probability) + '\n预测价 ' + money(item.price) + '\n预测时间 ' + when(item.time) + '\n模型 ' + (item.model_version || '—') + '\n仅表示60分钟先触墙概率' }));
      });
      plot.appendChild(group);
    });
  }
  const windowLiquidations = embeddedChart ? [] : window.BubbleChart.windowEvents(chart.liquidations, timeScale.start, timeScale.end).filter(event => event.notional_usd >= chart.liquidationMinimumUSD);
  const bubbles = embeddedChart ? { points: [], outside: 0 } : drawLiquidationBubbles(plot, windowLiquidations, range, timeScale, y, left, candleRight, top, plotBottom);
  const tradeMarkers = tradeView.enabled ? drawTradeMarkers(plot, tradeView.markers, range, timeScale, y, left, candleRight, top, plotBottom) : { visible: 0, outside: 0 };
  svg.setAttribute('data-bubble-count', bubbles.points.length);
  svg.setAttribute('data-trade-marker-count', tradeMarkers.visible);
  const tradeText = tradeView.historyLoading && !tradeView.historyLoaded
    ? ' · 历史开平点加载中'
    : tradeView.enabled
      ? ' · 历史开平点 ' + tradeExchangeSummary(tradeView.markers) + ' 共 ' + tradeView.markers.length + '个 · 屏内 ' + tradeMarkers.visible + '个 · 视野外 ' + tradeMarkers.outside + '个'
      : '';
  const liquidationFilterText = chart.liquidationMinimumUSD > 0 ? ' · 金额≥$' + compact(chart.liquidationMinimumUSD) : '';
  $('chart-context').textContent = context + liquidationFilterText + ' · 窗口已加载 ' + windowLiquidations.length + '笔 · 屏内气泡 ' + bubbles.points.length + '笔' + (bubbles.outside ? ' · 价格视野外 ' + bubbles.outside + '笔' : '') + tradeText + ' · ' + loadState;
  if (!embeddedChart && bins.length) {
    const maximum = Math.max(...bins.map(item => item.total_usd), 1);
    const binWidth = map?.bin_width || (high - low) / Math.max(1, bins.length);
    const barHeight = Math.max(2, Math.abs(y(low + binWidth) - y(low)) * .9);
    const leverageColors = { '10': '#7dd3fc', '25': '#6188ff', '50': '#f5c400', '100': '#f08833' };
    const topRanks = new Map();
    (map.top_long_liquidations || []).forEach((peak, index) => topRanks.set(String(peak.price), { label: '多' + (index + 1), color: '#fb7185' }));
    (map.top_short_liquidations || []).forEach((peak, index) => topRanks.set(String(peak.price), { label: '空' + (index + 1), color: '#2dd4bf' }));
    const overlay = svgNode('g', { 'clip-path': 'url(#liquidation-overlay-clip)', 'data-liquidation-overlay': '' });
    overlay.appendChild(svgNode('rect', { x: liquidationOverlayLeft, y: top, width: liquidationOverlayRight - liquidationOverlayLeft, height: plotBottom - top, fill: '#071019', 'fill-opacity': .34, stroke: '#315064', 'stroke-width': 1, rx: 3 }));
    const wall = svgNode('g', { 'clip-path': 'url(#liquidation-clip)' });
    bins.forEach(bin => {
      if (bin.price < low - binWidth || bin.price > high + binWidth) return;
      const width = Math.sqrt(bin.total_usd / maximum) * (liquidationRight - liquidationLeft) * chart.liquidationZoom;
      const color = bin.price < (map.mark_price || signal?.mark_price) ? '#fb7185' : '#2dd4bf';
      const leverageRows = Object.entries(bin.leverage_usd || {}).filter(([, amount]) => amount > 0).sort((a, b) => Number(a[0]) - Number(b[0]));
      const leverageTip = leverageRows.map(([leverage, amount]) => '\n' + leverage + 'x $' + compact(amount)).join('');
      const tip = liquidationName + zoomSuffix(chart.liquidationZoom) + '\n价格 ' + money(bin.price) + '\n总清算强度 $' + compact(bin.total_usd) + leverageTip + '\n多仓 $' + compact(bin.long_usd) + '\n空仓 $' + compact(bin.short_usd);
      if (leverageRows.length) {
        let cursor = liquidationLeft;
        leverageRows.forEach(([leverage, amount]) => {
          const segmentWidth = width * amount / bin.total_usd;
          wall.appendChild(svgNode('rect', { x: cursor, y: y(bin.price) - barHeight / 2, width: segmentWidth, height: barHeight, fill: leverageColors[leverage] || color, opacity: .4 + .58 * bin.total_usd / maximum, 'data-tip': tip, 'data-wall': 'segment' }));
          cursor += segmentWidth;
        });
      } else {
        wall.appendChild(svgNode('rect', { x: liquidationLeft, y: y(bin.price) - barHeight / 2, width, height: barHeight, fill: color, opacity: .35 + .6 * bin.total_usd / maximum, 'data-tip': tip, 'data-wall': 'bar' }));
      }
      const rank = topRanks.get(String(bin.price));
      if (rank) {
        wall.appendChild(svgNode('rect', { x: liquidationLeft, y: y(bin.price) - Math.max(3, barHeight / 2 + 1), width: Math.max(width, 25), height: Math.max(6, barHeight + 2), fill: 'none', stroke: rank.color, 'stroke-width': 1.4, opacity: .98, 'data-tip': rank.label + ' · ' + tip }));
        wall.appendChild(svgNode('text', { x: liquidationLeft + 3, y: y(bin.price) + 3, fill: '#f8fafc', 'font-size': 9, 'font-weight': 750, 'data-tip': rank.label + ' · ' + tip }, rank.label));
      }
    });
    overlay.appendChild(wall);
    plot.appendChild(overlay);
  }
  const volumeBins = embeddedChart ? [] : (volume?.bins || []);
  if (!embeddedChart && volumeBins.length) {
    const maximum = Math.max(...volumeBins.map(item => item.volume_usd), 1);
    const volumeGroup = svgNode('g', { 'clip-path': 'url(#volume-clip)' });
    volumeBins.forEach(bin => {
      const center = (bin.price_low + bin.price_high) / 2;
      if (bin.price_high < low || bin.price_low > high) return;
      const width = Math.sqrt(bin.volume_usd / maximum) * (volumeRight - volumeLeft) * chart.volumeZoom;
      const barHeight = Math.max(2, Math.abs(y(bin.price_low) - y(bin.price_high)) * .9);
      const topThree = bin.volume_rank >= 1 && bin.volume_rank <= 3;
      const fill = topThree ? '#a855f7' : '#60a5fa';
      const opacity = topThree ? .96 : bin.in_value_area ? .88 : .28;
      const rankTip = topThree ? '\n成交额排名 #' + bin.volume_rank : '';
      volumeGroup.appendChild(svgNode('rect', { x: volumeRight - width, y: y(center) - barHeight / 2, width, height: barHeight, fill, opacity, 'data-volume-bar': '', 'data-tip': 'Binance 当前时段成交量' + zoomSuffix(chart.volumeZoom) + '\n价格 ' + money(bin.price_low) + ' – ' + money(bin.price_high) + '\n成交额 $' + compact(bin.volume_usd) + '\n占比 ' + (bin.volume_percent || 0).toFixed(2) + '%' + rankTip + (bin.in_value_area ? '\n70%价值区域内' : '\n价值区域外') }));
    });
    plot.appendChild(volumeGroup);
  }
  svg.appendChild(plot);
  if (!embeddedChart) svg.appendChild(svgNode('text', { x: (volumeLeft + volumeRight) / 2, y: top + 12, 'text-anchor': 'middle', fill: '#7dd3fc', 'font-size': 11, 'font-weight': 650 }, '当前时段成交量' + zoomSuffix(chart.volumeZoom)));
  if (!embeddedChart) svg.appendChild(svgNode('text', { x: (liquidationLeft + liquidationRight) / 2, y: top + 12, 'text-anchor': 'middle', fill: coinGlassMap ? '#7dd3fc' : '#83a0b2', 'font-size': 11, 'font-weight': 650, 'data-liquidation-title': '' }, liquidationName + zoomSuffix(chart.liquidationZoom)));
  if (!embeddedChart && coinGlassMap) {
    const above = bins.filter(bin => bin.price > high).length;
    const below = bins.filter(bin => bin.price < low).length;
    if (above) svg.appendChild(svgNode('text', { x: liquidationLeft, y: top + 28, fill: '#7dd3fc', 'font-size': 11, 'data-liquidation-outside': 'above' }, '↑ 上方视野外 ' + above + '档'));
    if (below) svg.appendChild(svgNode('text', { x: liquidationLeft, y: plotBottom - 8, fill: '#7dd3fc', 'font-size': 11, 'data-liquidation-outside': 'below' }, '↓ 下方视野外 ' + below + '档'));
  }
  const currentPrice = view?.summary?.last_price || signal?.mark_price;
  const levels = embeddedChart ? [] : [['现价', currentPrice, '#f8fafc', '4 5']];
  levels.forEach(([name, price, color, dash]) => {
    if (!price || price < low || price > high) return;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: color, 'stroke-width': name === '现价' ? 1.2 : 1.8, 'stroke-dasharray': dash, opacity: .95 }));
    svg.appendChild(svgNode('rect', { x: profileRight - rightPriceLabelWidth, y: yy - 12, width: rightPriceLabelWidth, height: 22, fill: '#071019', stroke: color, rx: 5 }));
    svg.appendChild(svgNode('text', { x: profileRight - 6, y: yy + 4, 'text-anchor': 'end', fill: color, 'font-size': 11 }, name + ' ' + money(price)));
  });
  [['VAL', volume?.val, '#22c55e'], ['VAH', volume?.vah, '#ef4444']].forEach(([name, price, color]) => {
    if (embeddedChart) return;
    if (!finite(price) || price < low || price > high) return;
    const yy = y(price);
    svg.appendChild(svgNode('line', { x1: left, y1: yy, x2: profileRight, y2: yy, stroke: color, 'stroke-width': 1.7, opacity: .95 }));
    svg.appendChild(svgNode('rect', { x: profileRight - rightPriceLabelWidth, y: yy - 12, width: rightPriceLabelWidth, height: 22, fill: '#071019', stroke: color, rx: 5 }));
    svg.appendChild(svgNode('text', { x: profileRight - 6, y: yy + 4, 'text-anchor': 'end', fill: color, 'font-size': 11 }, name + ' ' + money(price)));
  });
  if (!embeddedChart) {
    drawLiquidationBiasArrow(svg, map, currentPrice, y, low, high, layout);
    drawLiquidationRankLabels(svg, map, low, high, y, top, plotBottom, layout);
  }
  bindTips(svg);
}

function bindTips(svg) {
  if (chartTipsBound) return;
  chartTipsBound = true;
  const tooltip = $('tooltip');
  const hideSoon = () => { clearTimeout(tooltipHideTimer); tooltipHideTimer = setTimeout(() => { tooltip.style.display = 'none'; }, 200); };
  svg.addEventListener('pointermove', event => {
    if (chart.drag) { tooltip.style.display = 'none'; return; }
    const tradeElement = event.target.closest('[data-trade-marker]');
    const element = tradeElement || event.target.closest('[data-tip]');
    const point = svgPoint(event);
    const overlayElement = event.target.closest('[data-liquidation-overlay], [data-liquidation-rank], [data-wall], [data-volume-bar]');
    const overlapping = tradeElement || overlayElement ? [] : (point.x >= dims.left && point.x <= dims.candleRight ? bubbleHits(point.x, point.y) : []);
    if (!element && !overlapping.length) { hideSoon(); return; }
    clearTimeout(tooltipHideTimer);
    tooltip.textContent = overlapping.length ? (overlapping.length > 1 ? '此处重叠 ' + overlapping.length + '笔（滚动查看）\n\n' : '') + overlapping.map(bubbleTip).join('\n\n') + '\n\n覆盖：本服务采集的 Binance 强平采样流，每交易对每秒最近一笔' : element.getAttribute('data-tip');
    tooltip.style.display = 'block';
    tooltip.style.left = Math.max(8, Math.min(innerWidth - tooltip.offsetWidth - 8, event.clientX + 14)) + 'px';
    tooltip.style.top = Math.max(8, Math.min(innerHeight - tooltip.offsetHeight - 8, event.clientY + 14)) + 'px';
  });
  svg.addEventListener('pointerleave', hideSoon);
}

function showFailures(failures) {
  const all = [...failures];
  if (streamError) all.push('实时推送 · ' + streamError);
  const tradeError = tradeViewError();
  if (tradeError) all.push('历史交易点 · ' + tradeError);
  $('api-errors').textContent = all.length ? '数据接口异常：\n' + all.join('\n') : '';
  $('api-errors').classList.toggle('hidden', all.length === 0);
}

async function loadOlder(options = {}) {
  const interval = options.interval || chart.interval;
  const targetSymbol = options.symbol || symbol;
  const background = options.background === true;
  const state = marketHistoryState(targetSymbol, interval);
  if (targetSymbol !== symbol || state.loading || manualMarketBackfill.active || (background && visibleMarketBackfill.controller)) return false;
  if (!background && automaticMarketBackfill.active && automaticMarketBackfill.current && automaticMarketBackfill.current !== interval) {
    moveMarketBackfillToFront(interval);
    return false;
  }
  if (background && state.complete) return false;
  if (!background && (chart.loadingOlder || (!state.initialized && !chart.candles.length) || (state.initialized && (!chart.hasMore || !chart.nextBefore)))) return false;
  if (background && state.initialized && (!state.hasMore || !state.nextBefore)) return false;

  const requestedSymbol = targetSymbol;
  const requestedInterval = interval;
  const cursor = state.initialized ? state.nextBefore : '';
  const previousCursor = cursor;
  const controller = new AbortController();
  state.loading = true;
  state.controller = controller;
  if (!background) {
    chart.loadingOlder = true;
    olderController = controller;
  }
  updateMarketBackfillButton();
  const timeout = setTimeout(() => controller.abort(), marketRequestTimeoutMs);
  const cutoff = Date.now();
  try {
    const view = await json(marketPath(cursor, { symbol: requestedSymbol, interval: requestedInterval, limit: background ? marketBackfillPageLimit : undefined }), { signal: controller.signal });
    if (controller.signal.aborted || requestedSymbol !== symbol) return false;
    const nextCursor = view?.next_before || '';
    const incomingCandles = Array.isArray(view?.candles) ? view.candles : [];
    if (!incomingCandles.length && (view?.has_more || nextCursor)) throw new Error('K线回补返回空响应');
    if (nextCursor && nextCursor === previousCursor) throw new Error('K线回补游标未前进');
    if (nextCursor && state.cursorHistory.has(nextCursor)) throw new Error('K线回补游标重复');
    if (previousCursor) state.cursorHistory.add(previousCursor);
    view.queryCutoff = cutoff;
    const activeNow = requestedInterval === chart.interval;
    if (!activeNow) {
      updateMarketHistoryState(state, view, !state.initialized, requestedSymbol);
    } else {
      mergeMarket(view, state.initialized, !state.initialized);
      draw(cache.market, cache.map, cache.signal, cache.volume);
      ensureWindowLiquidations();
    }
    state.complete = !view?.has_more || !nextCursor;
    state.retries = 0;
    state.retryAt = 0;
    return true;
  } catch (error) {
    if (controller.signal.aborted) return false;
    state.error = error.message || 'K线回补失败';
    if (!background) {
      lastFailures = lastFailures.filter(item => !item.startsWith('历史行情'));
      lastFailures.push('历史行情 · ' + state.error);
      showFailures(lastFailures);
    }
    return false;
  } finally {
    clearTimeout(timeout);
    state.loading = false;
    if (state.controller === controller) state.controller = null;
    if (olderController === controller) {
      chart.loadingOlder = false;
      olderController = null;
    }
    updateMarketBackfillButton();
  }
}

function applyMarketHistorySnapshot(state, snapshot, targetSymbol) {
  const activeNow = state.interval === chart.interval && targetSymbol === symbol;
  if (!activeNow) {
    updateMarketHistoryState(state, snapshot, true, targetSymbol);
    return;
  }
  const previous = {
    candles: chart.candles.slice(),
    signals: chart.signals.slice(),
    liquidations: chart.liquidations.slice(),
    liquidationsTruncated: chart.liquidationsTruncated,
    windowEnd: chart.windowEnd,
    atLatest: chart.atLatest,
    newData: chart.newData,
    liveView: cache.market
  };
  const anchor = !previous.atLatest && previous.candles.length
    ? previous.candles[Math.max(0, Math.min(previous.windowEnd, previous.candles.length) - 1)]?.time
    : '';
  updateMarketHistoryState(state, snapshot, true, targetSymbol);
  chart.candles = mergeCandles(state.candles, previous.candles);
  chart.signals = mergeSignals(state.signals, previous.signals);
  chart.liquidations = mergeLiquidations(state.liquidations, previous.liquidations, targetSymbol);
  chart.liquidationsTruncated = previous.liquidationsTruncated || state.liquidationsTruncated;
  chart.hasMore = false;
  chart.nextBefore = '';
  chart.atLatest = previous.atLatest;
  chart.newData = previous.newData;
  if (chart.atLatest) chart.windowEnd = chart.candles.length;
  else {
    const index = anchor ? chart.candles.findIndex(item => item.time === anchor) : -1;
    chart.windowEnd = index >= 0 ? index + 1 : Math.min(previous.windowEnd, chart.candles.length);
  }
  state.candles = chart.candles;
  state.signals = chart.signals;
  state.liquidations = chart.liquidations;
  state.liquidationsTruncated = chart.liquidationsTruncated;
  state.view = Object.assign({}, snapshot, previous.liveView || {}, {
    snapshot: true,
    candles: state.candles,
    model_signals: state.signals,
    liquidations: state.liquidations,
    liquidations_truncated: state.liquidationsTruncated,
    has_more: false,
    next_before: ''
  });
  cache.market = state.view;
  $('new-data').classList.toggle('hidden', !chart.newData);
  draw(cache.market, cache.map, cache.signal, cache.volume);
  ensureWindowLiquidations();
}

async function loadMarketHistorySnapshot(options = {}) {
  const interval = options.interval || chart.interval;
  const targetSymbol = options.symbol || symbol;
  const state = marketHistoryState(targetSymbol, interval);
  if (targetSymbol !== symbol || state.loading || manualMarketBackfill.active || visibleMarketBackfill.controller) return false;
  const controller = new AbortController();
  state.loading = true;
  state.controller = controller;
  updateMarketBackfillButton();
  const timeout = setTimeout(() => controller.abort(), marketRequestTimeoutMs * 4);
  try {
    const snapshot = await json(marketHistoryPath(interval, targetSymbol), { signal: controller.signal });
    if (controller.signal.aborted || targetSymbol !== symbol) return false;
    if (!Array.isArray(snapshot?.candles) || !snapshot.candles.length) throw new Error('服务端历史快照为空');
    snapshot.snapshot = true;
    snapshot.has_more = false;
    snapshot.next_before = '';
    snapshot.model_signals = Array.isArray(snapshot.model_signals) ? snapshot.model_signals : [];
    snapshot.liquidations = Array.isArray(snapshot.liquidations) ? snapshot.liquidations : [];
    applyMarketHistorySnapshot(state, snapshot, targetSymbol);
    state.retries = 0;
    state.retryAt = 0;
    return true;
  } catch (error) {
    if (controller.signal.aborted) return false;
    state.error = error.message || '服务端历史快照失败';
    return false;
  } finally {
    clearTimeout(timeout);
    state.loading = false;
    if (state.controller === controller) state.controller = null;
    updateMarketBackfillButton();
  }
}

function currentMarketGap() {
  const candles = visibleCandles();
  const interval = intervalMilliseconds[chart.interval] || 900000;
  if (candles.length < 2) return null;
  for (let index = 1; index < candles.length; index++) {
    const previous = Date.parse(candles[index - 1].time);
    const current = Date.parse(candles[index].time);
    if (!Number.isFinite(previous) || !Number.isFinite(current) || current - previous <= interval) continue;
    const pageEnd = Date.parse(candles.at(-1).time);
    return {
      before: new Date((Number.isFinite(pageEnd) ? pageEnd : current) + interval).toISOString(),
      limit: Math.min(500, Math.max(chart.visibleCount, candles.length))
    };
  }
  return null;
}

async function repairCurrentMarketGap() {
  const gap = currentMarketGap();
  if (!gap || chart.loadingOlder) return false;
  chart.loadingOlder = true;
  updateMarketBackfillButton();
  const requestedSymbol = symbol;
  const requestedInterval = chart.interval;
  const controller = new AbortController();
  olderController = controller;
  const timeout = setTimeout(() => controller.abort(), marketRequestTimeoutMs);
  const cutoff = Date.now();
  const wasAtLatest = chart.atLatest;
  const anchor = visibleCandles().at(-1)?.time || '';
  try {
    const view = await json(marketPath(gap.before, { limit: gap.limit, repair: true }), { signal: controller.signal });
    if (controller.signal.aborted || requestedSymbol !== symbol || requestedInterval !== chart.interval) return false;
    view.queryCutoff = cutoff;
    mergeMarket(view, false, false);
    if (!wasAtLatest && anchor) {
      const index = chart.candles.findIndex(item => item.time === anchor);
      if (index >= 0) chart.windowEnd = index + 1;
      chart.atLatest = false;
    }
    draw(cache.market, cache.map, cache.signal, cache.volume);
    ensureWindowLiquidations();
    return true;
  } catch (error) {
    if (controller.signal.aborted) return false;
    lastFailures = lastFailures.filter(item => !item.startsWith('历史行情'));
    lastFailures.push('历史行情回补 · ' + error.message);
    showFailures(lastFailures);
    return false;
  } finally {
    clearTimeout(timeout);
    if (olderController === controller) chart.loadingOlder = false;
    updateMarketBackfillButton();
  }
}

// Re-request the currently visible candle window.  The response may replace
// values at existing timestamps and repair internal gaps on the server, while
// the preserved chart state keeps a historical view, zoom, and manual axis in
// exactly the same place.
async function refillVisibleMarket() {
  if (visibleMarketBackfill.controller || chart.loadingOlder || !chart.candles.length) return false;
  const requestPath = visibleMarketPath();
  if (!requestPath) return false;
  const requestID = ++visibleMarketBackfill.sequence;
  const requestedSymbol = symbol;
  const requestedInterval = chart.interval;
  const preserved = {
    windowEnd: chart.windowEnd,
    atLatest: chart.atLatest,
    newData: chart.newData,
    yLow: chart.yLow,
    yHigh: chart.yHigh,
    autoSpan: chart.autoSpan,
    yManual: chart.yManual,
    hasMore: chart.hasMore,
    nextBefore: chart.nextBefore
  };
  const controller = new AbortController();
  refreshSequence++;
  refreshController?.abort();
  visibleMarketBackfill.controller = controller;
  const timeout = setTimeout(() => controller.abort(), marketRequestTimeoutMs);
  updateMarketBackfillButton();
  try {
    const [marketResult, volumeResult] = await Promise.allSettled([
      json(requestPath, { signal: controller.signal }),
      json('volume-profile/backfill?symbol=' + encodeURIComponent(symbol), { method: 'POST', signal: controller.signal })
    ]);
    if (marketResult.status !== 'fulfilled') throw marketResult.reason;
    if (volumeResult.status === 'fulfilled') {
      cache.volume = volumeResult.value;
      lastFailures = lastFailures.filter(item => !item.startsWith('成交量分布回补'));
    } else if (!controller.signal.aborted) {
      lastFailures = lastFailures.filter(item => !item.startsWith('成交量分布回补'));
      lastFailures.push('成交量分布回补 · ' + volumeResult.reason.message);
    }
    const view = marketResult.value;
    if (controller.signal.aborted || requestID !== visibleMarketBackfill.sequence || requestedSymbol !== symbol || requestedInterval !== chart.interval) return false;
    view.queryCutoff = Date.now();
    mergeMarket(view, false, false);
    chart.windowEnd = preserved.atLatest
      ? chart.candles.length
      : Math.max(0, Math.min(preserved.windowEnd, chart.candles.length));
    chart.atLatest = preserved.atLatest;
    chart.newData = preserved.newData;
    chart.yLow = preserved.yLow;
    chart.yHigh = preserved.yHigh;
    chart.autoSpan = preserved.autoSpan;
    chart.yManual = preserved.yManual;
    chart.hasMore = preserved.hasMore;
    chart.nextBefore = preserved.nextBefore;
    $('new-data').classList.toggle('hidden', !chart.newData);
    draw(cache.market, cache.map, cache.signal, cache.volume);
    showFailures(lastFailures);
    ensureWindowLiquidations();
    return true;
  } catch (error) {
    if (controller.signal.aborted) return false;
    lastFailures = lastFailures.filter(item => !item.startsWith('当前K线回补'));
    lastFailures.push('当前K线回补 · ' + error.message);
    showFailures(lastFailures);
    return false;
  } finally {
    clearTimeout(timeout);
    if (visibleMarketBackfill.controller === controller) visibleMarketBackfill.controller = null;
    updateMarketBackfillButton();
  }
}

async function refresh(resetMarket = false) {
  if (manualMarketBackfill.active) return;
  const sequence = ++refreshSequence;
  refreshController?.abort();
  const controller = new AbortController();
  refreshController = controller;
  const cutoff = Date.now();
  const requestedSymbol = symbol;
  const requestedInterval = chart.interval;
  refreshing = true;
  try {
    const specs = embeddedChart
      ? [['market', '市场行情', marketPath()]]
      : [['signal', '预测', 'signals/latest?symbol=' + symbol], ['map', '清算地图', 'map?symbol=' + symbol], ['volume', '成交量分布', 'volume-profile?symbol=' + symbol], ['market', '市场行情', marketPath()], ['schedule', '抓取计划', 'coinglass/schedule']];
    const results = await Promise.allSettled(specs.map(item => json(item[2], { signal: controller.signal })));
    if (requestedSymbol !== symbol || requestedInterval !== chart.interval || sequence !== refreshSequence) return;
    lastFailures = [];
    results.forEach((result, index) => {
      const [key, label] = specs[index];
      if (result.status === 'fulfilled') {
        if (key === 'market') {
          result.value.queryCutoff = cutoff;
          const reset = resetMarket || !chart.candles.length;
          mergeMarket(result.value, false, reset);
        }
        else if (key === 'schedule') cache.schedule = { ...result.value, received_at_ms: Date.now() };
        else cache[key] = result.value;
      } else {
        if (key === 'map') cache.map = null;
        lastFailures.push(label + ' · ' + result.reason.message);
      }
    });
    if (cache.market) updateTicker(cache.market);
    if (!embeddedChart && cache.signal) {
      $('reason').textContent = cache.signal.state === 'ok' ? '当前领先类别：' + (cache.signal.leading_class || '—') + '；触发顺序：' + (cache.signal.trigger_order || 'pending') + '。' : (cache.signal.reason || '清算墙或历史数据不足');
      $('model-info').textContent = cache.signal.state === 'disabled' ? '模型：已停用（本地信号计算已停用）' : '模型：' + (cache.signal.model_version || '尚未训练') + ' · 数据年龄 ' + (finite(cache.signal.data_age_seconds) ? Math.max(0, cache.signal.data_age_seconds).toFixed(0) + ' 秒' : '—');
    }
    draw(cache.market, cache.map, cache.signal, cache.volume);
    if (!embeddedChart) {
      ensureWindowLiquidations();
      updateCoinGlassCountdown();
    }
    showFailures(lastFailures);
    if (!embeddedChart && cache.market) scheduleAutomaticMarketBackfill(requestedInterval);
  } finally {
    if (sequence === refreshSequence) refreshing = false;
  }
}

function svgPoint(event) {
  const rect = $('market-chart').getBoundingClientRect();
  return { x: (event.clientX - rect.left) * dims.width / rect.width, y: (event.clientY - rect.top) * dims.height / rect.height };
}

let marketBackfillResumeTimer = 0;
function scheduleMarketBackfillResume(refreshLatest = false) {
  clearTimeout(marketBackfillResumeTimer);
  marketBackfillResumeTimer = setTimeout(() => {
    if (!cache.market || embeddedChart) return;
    draw(cache.market, cache.map, cache.signal, cache.volume);
    scheduleAutomaticMarketBackfill(chart.interval);
    if (refreshLatest && !document.hidden) refresh(false);
  }, 120);
}

// chartRegion is the wheel routing table. The vertical wheel means something different in
// every column of this chart, so the hovered x decides which series the gesture scales.
function chartRegion(point) {
  if (point.x >= dims.liquidationOverlayLeft && point.x <= dims.liquidationOverlayRight) return 'liquidation';
  if (point.x >= dims.left && point.x <= dims.candleRight) return 'candles';
  if (point.x >= dims.volumeLeft && point.x <= dims.volumeRight) return 'volume';
  return 'price';
}

// zoomCandleWindow resizes the candle strip around the candle under the cursor. windowEnd
// is a candle index, so the continuous index below the pointer is held fixed while the
// visible count scales, which mirrors how the pointer drag pans the same index space.
function zoomCandleWindow(fraction, factor) {
  const total = chart.candles.length;
  if (!total) return false;
  const right = 1 - Math.max(0, Math.min(1, fraction));
  const next = Math.round(Math.max(candleZoomLimits.minimum, Math.min(candleZoomLimits.maximum, chart.visibleCount * factor)));
  if (next === chart.visibleCount) return false;
  const anchor = chart.windowEnd - right * chart.visibleCount;
  chart.visibleCount = next;
  chart.windowEnd = Math.round(Math.max(Math.min(next, total), Math.min(total, anchor + right * next)));
  chart.atLatest = chart.windowEnd === total;
  if (!chart.atLatest) chart.newData = false;
  $('new-data').classList.toggle('hidden', !chart.newData);
  return true;
}

// zoomProfilePanel scales one profile column's bar lengths. The columns render their own
// data, so zooming them never asks the server for anything.
function zoomProfilePanel(region, factor) {
  const key = region === 'volume' ? 'volumeZoom' : 'liquidationZoom';
  const next = Math.max(profileZoomLimits.minimum, Math.min(profileZoomLimits.maximum, chart[key] * factor));
  if (next === chart[key]) return false;
  chart[key] = next;
  return true;
}

// zoomPriceAxis keeps the gesture this wheel handler used to perform unconditionally: it
// stays available on shift + wheel and while the cursor sits over a gutter or the axis.
function zoomPriceAxis(point, factor) {
  if (!finite(chart.yLow) || !finite(chart.yHigh)) return false;
  const plotHeight = dims.height - dims.top - dims.bottom;
  const anchor = chart.yHigh - (point.y - dims.top) / plotHeight * (chart.yHigh - chart.yLow);
  const current = cache.market?.summary?.last_price || cache.signal?.mark_price || 1;
  const minSpan = Math.max(current * .001, 1e-8);
  const maxSpan = Math.max((chart.autoSpan || chart.yHigh - chart.yLow) * 8, minSpan);
  const oldSpan = chart.yHigh - chart.yLow;
  const span = Math.min(maxSpan, Math.max(minSpan, oldSpan * factor));
  const ratio = (anchor - chart.yLow) / oldSpan;
  chart.yLow = anchor - span * ratio;
  chart.yHigh = chart.yLow + span;
  chart.yManual = true;
  return true;
}

function normalizedLiquidationMinimum(value) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : null;
}

function normalizedLiquidationOffset(value) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(liquidationOffsetLimits.minimum, Math.min(liquidationOffsetLimits.maximum, number)) : null;
}

function applyLiquidationMinimum() {
  const input = $('liquidation-minimum');
  const minimum = normalizedLiquidationMinimum(input?.value);
  if (minimum === null) {
    if (input) input.value = String(chart.liquidationMinimumUSD);
    return false;
  }
  if (minimum === chart.liquidationMinimumUSD) return true;
  chart.liquidationMinimumUSD = minimum;
  try { localStorage.setItem('liquidation.minimumUSD', String(minimum)); } catch (_) { /* browser storage is optional */ }
  bubbleLoader.reset();
  draw(cache.market, cache.map, cache.signal, cache.volume);
  ensureWindowLiquidations();
  return true;
}

function applyLiquidationOffsets() {
  const longInput = $('liquidation-long-offset');
  const shortInput = $('liquidation-short-offset');
  const long = normalizedLiquidationOffset(longInput?.value);
  const short = normalizedLiquidationOffset(shortInput?.value);
  if (long === null || short === null) {
    if (longInput) longInput.value = String(chart.liquidationLongOffset);
    if (shortInput) shortInput.value = String(chart.liquidationShortOffset);
    return false;
  }
  chart.liquidationLongOffset = long;
  chart.liquidationShortOffset = short;
  try {
    localStorage.setItem('liquidation.longOffset', String(long));
    localStorage.setItem('liquidation.shortOffset', String(short));
  } catch (_) { /* browser storage is optional */ }
  draw(cache.market, cache.map, cache.signal, cache.volume);
  return true;
}

function installLiquidationControls() {
  const minimum = $('liquidation-minimum');
  const minimumApply = $('liquidation-minimum-apply');
  const longOffset = $('liquidation-long-offset');
  const shortOffset = $('liquidation-short-offset');
  if (!minimum || !longOffset || !shortOffset) return;
  minimum.value = String(chart.liquidationMinimumUSD);
  longOffset.min = String(liquidationOffsetLimits.minimum);
  longOffset.max = String(liquidationOffsetLimits.maximum);
  shortOffset.min = String(liquidationOffsetLimits.minimum);
  shortOffset.max = String(liquidationOffsetLimits.maximum);
  longOffset.value = String(chart.liquidationLongOffset);
  shortOffset.value = String(chart.liquidationShortOffset);
  minimumApply?.addEventListener('click', applyLiquidationMinimum);
  minimum.addEventListener('change', applyLiquidationMinimum);
  minimum.addEventListener('keydown', event => {
    if (event.key === 'Enter') { event.preventDefault(); applyLiquidationMinimum(); }
  });
  [longOffset, shortOffset].forEach(input => {
    input.addEventListener('change', applyLiquidationOffsets);
    input.addEventListener('keydown', event => {
      if (event.key === 'Enter') { event.preventDefault(); applyLiquidationOffsets(); }
    });
  });
}

function normalizedChartHeight(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return null;
  return Math.max(chartHeightLimits.minimum, Math.min(chartHeightLimits.maximum, Math.round(number)));
}

function setChartHeight(value, persist = true, render = true) {
  const height = normalizedChartHeight(value);
  if (height === null) return false;
  chart.manualHeight = height;
  const wrap = document.querySelector('.chart-wrap');
  if (wrap) wrap.style.height = height + 'px';
  const input = $('chart-height');
  if (input) input.value = String(height);
  if (persist) {
    try {
      localStorage.setItem('liquidation.chartHeight', String(height));
    } catch (_) { /* browser storage is optional */ }
  }
  if (render) draw(cache.market, cache.map, cache.signal, cache.volume);
  return true;
}

function useAutomaticChartHeight(render = true) {
  const wrap = document.querySelector('.chart-wrap');
  if (!wrap) return;
  const previousHeight = chart.manualHeight;
  chart.manualHeight = null;
  wrap.style.removeProperty('height');
  const height = calculateBubbleChartViewportHeight();
  if (height === null) {
    chart.manualHeight = previousHeight;
    return;
  }
  setChartHeight(height, true, render);
}

function installChartSizeControls() {
  const wrap = document.querySelector('.chart-wrap');
  const handle = $('chart-resize-handle');
  const input = $('chart-height');
  if (!wrap || !handle || !input) return;
  input.min = String(chartHeightLimits.minimum);
  input.max = String(chartHeightLimits.maximum);
  if (Number.isFinite(chart.manualHeight)) wrap.style.height = chart.manualHeight + 'px';
  input.value = String(chart.manualHeight || Math.round(wrap.getBoundingClientRect().height || 874));
  const applyInput = () => {
    if (!setChartHeight(input.value)) input.value = String(chart.manualHeight || Math.round(wrap.getBoundingClientRect().height || 874));
  };
  input.addEventListener('change', applyInput);
  input.addEventListener('keydown', event => {
    if (event.key === 'Enter') { event.preventDefault(); applyInput(); }
  });
  $('chart-height-apply').addEventListener('click', applyInput);
  $('chart-height-auto').addEventListener('click', () => useAutomaticChartHeight());

  let resize = null;
  handle.addEventListener('pointerdown', event => {
    event.preventDefault();
    resize = { pointerId: event.pointerId, startY: event.clientY, startHeight: wrap.getBoundingClientRect().height };
    handle.setPointerCapture(event.pointerId);
    wrap.classList.add('resizing');
  });
  handle.addEventListener('pointermove', event => {
    if (!resize || resize.pointerId !== event.pointerId) return;
    setChartHeight(resize.startHeight + event.clientY - resize.startY, false);
  });
  const endResize = event => {
    if (!resize || resize.pointerId !== event.pointerId) return;
    setChartHeight(wrap.getBoundingClientRect().height, true, false);
    resize = null;
    wrap.classList.remove('resizing');
  };
  handle.addEventListener('pointerup', endResize);
  handle.addEventListener('pointercancel', endResize);
  handle.addEventListener('keydown', event => {
    if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
      event.preventDefault();
      setChartHeight(Number(input.value) + (event.key === 'ArrowUp' ? 10 : -10));
    } else if (event.key === 'Home') {
      event.preventDefault();
      setChartHeight(chartHeightLimits.minimum);
    } else if (event.key === 'End') {
      event.preventDefault();
      setChartHeight(chartHeightLimits.maximum);
    }
  });
}

function installChartInteractions() {
  const svg = $('market-chart');
  const wrap = svg.parentElement;
  svg.addEventListener('wheel', event => {
    event.preventDefault();
    const point = svgPoint(event);
    // One tick resizes a series by 12%. Wheel down widens the candle window and the price span,
    // which is the same direction the profile bars read as shrinking, so they take the reciprocal.
    const factor = event.deltaY > 0 ? 1.12 : 1 / 1.12;
    const region = event.shiftKey || event.ctrlKey ? 'price' : chartRegion(point);
    if (region === 'candles') {
      if (!zoomCandleWindow((point.x - dims.left) / (dims.candleRight - dims.left), factor)) return;
      draw(cache.market, cache.map, cache.signal, cache.volume);
      ensureWindowLiquidations();
      // Zooming out past the loaded page needs the same history paging a left drag does.
      if (chart.windowEnd <= chart.visibleCount + 12) loadOlder();
      return;
    }
    if (region === 'volume' || region === 'liquidation') {
      if (zoomProfilePanel(region, 1 / factor)) draw(cache.market, cache.map, cache.signal, cache.volume);
      return;
    }
    if (zoomPriceAxis(point, factor)) draw(cache.market, cache.map, cache.signal, cache.volume);
  }, { passive: false });
  svg.addEventListener('pointerdown', event => {
    const point = svgPoint(event);
    let mode = '';
    if (point.x >= dims.liquidationOverlayLeft && point.x <= dims.liquidationOverlayRight) mode = 'price';
    else if (point.x >= dims.left && point.x <= dims.candleRight) mode = 'chart';
    else if (point.x >= dims.volumeLeft - candleToVolumeGap && point.x <= dims.profileRight) mode = 'price';
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
    if (Math.abs(point.y - chart.drag.y) > 1 && (chart.drag.mode === 'chart' || chart.drag.mode === 'price')) {
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
    ensureWindowLiquidations();
  };
  svg.addEventListener('pointerup', endDrag);
  svg.addEventListener('pointercancel', endDrag);
}

function installEmbeddedChartInteractions() {
  const svg = $('market-chart');
  if (!svg) return;
  const wrap = svg.parentElement;
  svg.addEventListener('wheel', event => {
    event.preventDefault();
    const point = svgPoint(event);
    const fraction = (point.x - dims.left) / (embeddedChartCandleRight - dims.left);
    const factor = event.deltaY > 0 ? 1.12 : 1 / 1.12;
    if (!zoomCandleWindow(fraction, factor)) return;
    draw(cache.market, cache.map, cache.signal, cache.volume);
    if (chart.windowEnd <= chart.visibleCount + 12) loadOlder();
  }, { passive: false });
  svg.addEventListener('pointerdown', event => {
    if (event.button !== undefined && event.button !== 0) return;
    const point = svgPoint(event);
    if (!chart.candles.length || point.x < dims.left || point.x > embeddedChartCandleRight) return;
    event.preventDefault();
    chart.drag = { mode: 'chart', pointerId: event.pointerId, x: point.x, end: chart.windowEnd };
    svg.setPointerCapture(event.pointerId);
    wrap.classList.add('dragging');
  });
  svg.addEventListener('pointermove', event => {
    if (!chart.drag || chart.drag.pointerId !== event.pointerId) return;
    const point = svgPoint(event);
    const step = (embeddedChartCandleRight - dims.left) / chart.visibleCount;
    const shift = Math.round((chart.drag.x - point.x) / step);
    const minimum = Math.min(chart.visibleCount, chart.candles.length);
    chart.windowEnd = Math.max(minimum, Math.min(chart.candles.length, chart.drag.end + shift));
    chart.atLatest = chart.windowEnd === chart.candles.length;
    if (!chart.atLatest) chart.newData = false;
    $('new-data').classList.toggle('hidden', !chart.newData);
    draw(cache.market, cache.map, cache.signal, cache.volume);
  });
  const endDrag = event => {
    if (!chart.drag || chart.drag.pointerId !== event.pointerId) return;
    if (chart.windowEnd <= chart.visibleCount + 12) loadOlder();
    chart.drag = null;
    wrap.classList.remove('dragging');
  };
  svg.addEventListener('pointerup', endDrag);
  svg.addEventListener('pointercancel', endDrag);
}

function restoreCachedMarketInterval(interval = chart.interval) {
  const state = marketHistoryCache.get(marketHistoryKey(symbol, interval));
  if (!state?.initialized || !state.candles.length || !state.view) return false;
  chart.candles = state.candles;
  chart.signals = state.signals;
  chart.liquidations = state.liquidations;
  chart.liquidationsTruncated = state.liquidationsTruncated;
  chart.windowEnd = chart.candles.length;
  chart.hasMore = state.hasMore;
  chart.nextBefore = state.nextBefore;
  chart.atLatest = true;
  chart.newData = false;
  cache.market = state.view;
  return true;
}

function resetChartState(options = {}) {
  bubbleLoader.reset();
  if (options.cancelBackfill) cancelAutomaticMarketBackfill(true);
  refreshController?.abort();
  visibleMarketBackfill.sequence++;
  visibleMarketBackfill.controller?.abort();
  visibleMarketBackfill.controller = null;
  manualMarketBackfill.active = false;
  chart.loadingOlder = false;
  chart.candles = [];
  chart.signals = [];
  chart.liquidations = [];
  chart.liquidationsTruncated = false;
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
  updateMarketBackfillButton();
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
    activateSymbol(button.dataset.symbol);
  });
});
if ($('symbol-load')) {
  $('symbol-load').addEventListener('click', () => {
    const next = catalogSymbol($('symbol-search').value);
    if (!next) {
      $('symbol-catalog-state').textContent = '请输入目录中的币对，例如 BTCUSDT';
      return;
    }
    activateSymbol(next);
  });
  $('symbol-search').addEventListener('keydown', event => {
    if (event.key === 'Enter') {
      event.preventDefault();
      $('symbol-load').click();
    }
  });
}
document.querySelectorAll('button[data-interval]').forEach(button => {
  button.classList.toggle('active', button.dataset.interval === chart.interval);
  button.addEventListener('click', () => {
    if (chart.interval === button.dataset.interval) return;
    chart.interval = button.dataset.interval;
    try { localStorage.setItem('liquidation.interval', chart.interval); } catch (_) { /* keep current interval */ }
    document.querySelectorAll('button[data-interval]').forEach(item => item.classList.toggle('active', item === button));
    resetChartState();
    const restored = restoreCachedMarketInterval(chart.interval);
    if (restored) {
      draw(cache.market, cache.map, cache.signal, cache.volume);
      ensureWindowLiquidations();
    }
    refresh(!restored);
    scheduleAutomaticMarketBackfill(chart.interval);
  });
});
$('chart-latest').addEventListener('click', () => {
  chart.atLatest = true;
  chart.newData = false;
  tradeView.focusAt = NaN;
  $('new-data').classList.add('hidden');
  resetChartState();
  const restored = restoreCachedMarketInterval(chart.interval);
  if (restored) {
    draw(cache.market, cache.map, cache.signal, cache.volume);
    ensureWindowLiquidations();
  }
  refresh(!restored);
  scheduleAutomaticMarketBackfill(chart.interval);
});
$('chart-reset').addEventListener('click', () => {
  chart.yManual = false;
  chart.yLow = chart.yHigh = chart.autoSpan = null;
  chart.visibleCount = defaultVisibleCandles;
  chart.volumeZoom = chart.liquidationZoom = 1;
  chart.windowEnd = Math.max(Math.min(chart.windowEnd, chart.candles.length), 0);
  chart.atLatest = chart.windowEnd === chart.candles.length;
  draw(cache.market, cache.map, cache.signal, cache.volume);
  ensureWindowLiquidations();
});
async function manualBackfillMarketHistory() {
  const button = $('market-backfill');
  if (!button || manualMarketBackfill.active || button.disabled) return;
  const resumeAutomatic = automaticMarketBackfill.active && automaticMarketBackfill.symbol === symbol;
  const requestSequence = visibleMarketBackfill.sequence;
  const requestedSymbol = symbol;
  const requestedInterval = chart.interval;
  manualMarketBackfill.active = true;
  updateMarketBackfillButton();
  try {
    if (resumeAutomatic) await pauseAutomaticMarketBackfillForManual();
    if (requestSequence !== visibleMarketBackfill.sequence || requestedSymbol !== symbol || requestedInterval !== chart.interval) return;
    await refillVisibleMarket();
  } finally {
    manualMarketBackfill.active = false;
    if (resumeAutomatic) resumeAutomaticMarketBackfill();
    updateMarketBackfillButton();
  }
}
$('market-backfill').addEventListener('click', manualBackfillMarketHistory);
$('bubble-retry').addEventListener('click', () => ensureWindowLiquidations(true));
$('tooltip').addEventListener('pointerenter', () => clearTimeout(tooltipHideTimer));
$('tooltip').addEventListener('pointerleave', () => { $('tooltip').style.display = 'none'; });
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

installLiquidationControls();
updateMarketBackfillButton();
if (!embeddedChart) installChartSizeControls();
if (!embeddedChart) installChartInteractions();
if (embeddedChart) installEmbeddedChartInteractions();
if (embeddedChart) refresh(true);
else {
  loadHistoricalTradeView();
  loadSymbolCatalog();
  refresh(true);
}
setInterval(() => refresh(false), 10000);
setInterval(updateCoinGlassCountdown, 1000);
setInterval(() => { if (!$('log-drawer').classList.contains('hidden') && $('log-auto').checked) loadLogs(false); }, 5000);

const events = new EventSource(api('stream'));
let streamNeedsCalibration = false;
const recoverStream = () => {
  streamError = '';
  showFailures(lastFailures);
  if (streamNeedsCalibration) { streamNeedsCalibration = false; refresh(false); }
};
events.onopen = recoverStream;
events.addEventListener('health', recoverStream);
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
events.onerror = () => { streamNeedsCalibration = true; streamError = '连接中断，正在重试'; showFailures(lastFailures); };
window.addEventListener('themechange', () => draw(cache.market, cache.map, cache.signal, cache.volume));
window.addEventListener('resize', () => scheduleMarketBackfillResume(true));
window.visualViewport?.addEventListener('resize', () => scheduleMarketBackfillResume(true));
window.addEventListener('focus', () => scheduleMarketBackfillResume(true));
window.addEventListener('pageshow', () => scheduleMarketBackfillResume(true));
window.addEventListener('online', () => scheduleMarketBackfillResume(true));
document.addEventListener('visibilitychange', () => scheduleMarketBackfillResume(!document.hidden));
