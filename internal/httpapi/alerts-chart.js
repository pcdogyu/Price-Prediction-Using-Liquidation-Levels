(function (root) {
  'use strict';
  const finite = value => typeof value === 'number' && Number.isFinite(value);

  // The chart offers the periods the alert strategy is readable against.
  // Binance serves all of them natively except 2m, which the server aggregates
  // from 1m candles before returning it to the browser.
  const INTERVALS = ['1m', '2m', '3m', '5m', '15m', '30m', '1h', '4h', '8h', '12h'];
  const INTERVAL_MS = { '1m': 6e4, '2m': 12e4, '3m': 18e4, '5m': 3e5, '15m': 9e5, '30m': 18e5, '1h': 36e5, '4h': 144e5, '8h': 288e5, '12h': 432e5 };
  const DEFAULT_VISIBLE = 150, MIN_VISIBLE = 12, MAX_VISIBLE = 800, RIGHT_PAD = 2;
  const HISTORY_TRIGGER = 25, CANDLE_REFRESH_MS = 5000, SIGNAL_REFRESH_MS = 15000, SIGNAL_PAGES = 3;
  const STORE_INTERVAL = 'liquidation.kline.interval', STORE_SIGNALS = 'liquidation.kline.signals';

  const intervalMs = name => INTERVAL_MS[name] || 0;
  const bucketStart = (time, step) => step > 0 ? Math.floor(time / step) * step : time;

  // fitWindow returns the newest window that shows count candles, keeping a small
  // empty margin on the right so the forming candle never sits against the axis.
  function fitWindow(newestTime, step, count) {
    const end = newestTime + (RIGHT_PAD + 1) * step;
    return { start: end - Math.max(1, count) * step, end };
  }

  // clampWindow keeps the span inside the zoom limits and the window inside the
  // data: the right edge stops at the newest candle margin, while the left edge may
  // run one screen into not yet fetched history because that gap triggers a load.
  function clampWindow(range, limits) {
    const span = Math.max(limits.minSpan, Math.min(limits.maxSpan, range.end - range.start));
    let start = range.start, end = start + span;
    if (end > limits.maxEnd) { end = limits.maxEnd; start = end - span; }
    if (start < limits.minStart) { start = limits.minStart; end = start + span; }
    return { start, end };
  }

  // zoomWindow scales the time span around the cursor time so the candle under the
  // pointer stays under the pointer, which is what makes wheel zoom feel anchored.
  function zoomWindow(range, anchor, factor, limits) {
    const span = range.end - range.start;
    const next = Math.max(limits.minSpan, Math.min(limits.maxSpan, span * factor));
    const ratio = span > 0 ? (anchor - range.start) / span : .5;
    return clampWindow({ start: anchor - next * ratio, end: anchor - next * ratio + next }, limits);
  }

  const panWindow = (range, delta, limits) => clampWindow({ start: range.start + delta, end: range.end + delta }, limits);

  function visibleCandles(candles, range, step) {
    return candles.filter(candle => candle.t + step > range.start && candle.t < range.end);
  }

  // priceRange fits the visible candles with padding and keeps a flat series open.
  function priceRange(candles, range, step) {
    let low = Infinity, high = -Infinity;
    for (const candle of visibleCandles(candles, range, step)) {
      if (!finite(candle.low) || !finite(candle.high) || candle.low <= 0 || candle.high < candle.low) continue;
      low = Math.min(low, candle.low);
      high = Math.max(high, candle.high);
    }
    if (!Number.isFinite(low) || !Number.isFinite(high)) return null;
    if (high === low) { const half = Math.max(low * .001, 1e-9); low -= half; high += half; }
    const padding = (high - low) * .08;
    return [low - padding, high + padding];
  }

  // zoomPriceRange changes the visible price span around the pointer price.
  // Keeping the anchor's relative position stable makes the price-axis wheel
  // behave like the time-axis zoom instead of jumping to the middle.
  function zoomPriceRange(range, anchor, factor, limits) {
    if (!Array.isArray(range) || range.length < 2 || !finite(range[0]) || !finite(range[1]) || range[1] <= range[0]) return null;
    const span = range[1] - range[0];
    const next = Math.max(limits.minSpan, Math.min(limits.maxSpan, span * factor));
    const ratio = (anchor - range[0]) / span;
    const low = anchor - next * ratio;
    return [low, low + next];
  }

  // dragPriceRange maps vertical price-axis movement to a zoom factor while
  // keeping the price under the initial pointer position anchored. Dragging up
  // narrows the range; dragging down widens it.
  function dragPriceRange(range, anchor, delta, extent, limits) {
    if (!finite(delta) || !finite(extent) || extent <= 0) return null;
    return zoomPriceRange(range, anchor, Math.exp(delta / extent * 2), limits);
  }

  function volumeMax(candles, range, step) {
    let peak = 0;
    for (const candle of visibleCandles(candles, range, step)) if (finite(candle.volume) && candle.volume > peak) peak = candle.volume;
    return peak;
  }

  // radarZone mirrors the server's 1m VWAP zone calculation when the local
  // candle cache is the only available source. The server normally supplies a
  // more precise 1m result; this fallback keeps the chart informative while
  // the local candle window is still warming up.
  function radarZone(candles, periodMinutes, zThreshold, intervalMinutes) {
    const minutes = Number(intervalMinutes);
    const period = Number(periodMinutes);
    const threshold = Number(zThreshold);
    if (!Array.isArray(candles) || !Number.isFinite(minutes) || minutes <= 0 || !Number.isFinite(period) || period <= 0 || !Number.isFinite(threshold) || threshold <= 0) return null;
    const bars = Math.ceil(period / minutes);
    if (bars < 2 || candles.length < bars) return null;
    const window = candles.slice(-bars);
    let weight = 0, vwap = 0, volumeWeighted = false;
    for (const candle of window) {
      if (!finite(candle.close) || candle.close <= 0) continue;
      const volume = finite(candle.volume) && candle.volume > 0 ? candle.volume : 0;
      weight += volume;
      vwap += candle.close * volume;
    }
    volumeWeighted = weight > 0;
    if (!volumeWeighted) {
      for (const candle of window) {
        if (finite(candle.close) && candle.close > 0) { vwap += candle.close; weight += 1; }
      }
    }
    if (weight <= 0) return null;
    vwap /= weight;
    let variance = 0;
    for (const candle of window) {
      if (!finite(candle.close) || candle.close <= 0) continue;
      const rawVolume = finite(candle.volume) && candle.volume > 0 ? candle.volume : 0;
      const volume = volumeWeighted ? rawVolume : 1;
      variance += volume * (candle.close - vwap) * (candle.close - vwap);
    }
    const sigma = Math.sqrt(variance / weight);
    const price = window[window.length - 1].close;
    if (!finite(sigma) || sigma <= 0 || !finite(price) || price <= 0) return null;
    return { ready: true, period_minutes: period, z_score_threshold: threshold, vwap, sigma, current_price: price,
      current_z_score: (price - vwap) / sigma, upper_open_price: vwap + threshold * sigma, lower_open_price: vwap - threshold * sigma };
  }

  function niceTicks(low, high, target) {
    if (!finite(low) || !finite(high) || high <= low || target < 1) return [];
    const magnitude = Math.pow(10, Math.floor(Math.log10((high - low) / target)));
    const step = [1, 2, 2.5, 5, 10].map(multiplier => multiplier * magnitude).find(candidate => (high - low) / candidate <= target) || 10 * magnitude;
    const ticks = [];
    for (let value = Math.ceil(low / step) * step; value <= high; value += step) ticks.push(value);
    return ticks;
  }

  // mergeCandles joins pages by candle time, the newer arrival winning.
  function mergeCandles(current, incoming) {
    const byTime = new Map();
    for (const candle of [...current, ...incoming]) byTime.set(candle.t, candle);
    return Array.from(byTime.values()).sort((a, b) => a.t - b.t);
  }

  // normaliseCandles turns API rows into numeric candles and drops broken ones,
  // because the klines endpoint can carry a still empty forming candle.
  function normaliseCandles(rows) {
    const values = [];
    for (const row of rows || []) {
      const at = Date.parse(row && row.time);
      if (!Number.isFinite(at) || ![row.open, row.high, row.low, row.close].every(finite)) continue;
      values.push({ t: at, open: row.open, high: row.high, low: row.low, close: row.close, volume: finite(row.volume_usd) ? row.volume_usd : 0 });
    }
    return values;
  }

  // filterSignals keeps one entry per signal id and applies the kind toggles.
  function filterSignals(signals, options) {
    const byId = new Map();
    for (const signal of signals) {
      if (!signal || !signal.id || !Number.isFinite(signal.t)) continue;
      if (!options.aggressive && signal.kind !== 'steady') continue;
      if (!options.steady && signal.kind === 'steady') continue;
      byId.set(signal.id, signal);
    }
    return Array.from(byId.values()).sort((a, b) => a.t - b.t);
  }

  // markerGroups collapses the signals that fall inside the same candle, kind and
  // direction into one marker, so a busy hour stays readable and shows a count.
  // The amount is kept alongside the group because the tooltip represents every
  // signal collapsed into that marker, not just the first one.
  function markerGroups(signals, step) {
    const groups = new Map();
    for (const signal of signals) {
      const bucket = bucketStart(signal.t, step);
      const kind = signal.kind === 'steady' ? 'steady' : 'aggressive';
      const side = signal.side === 'short' ? 'short' : 'long';
      const peakDecay = signal.peakDecay === true;
      const key = bucket + '|' + kind + '|' + side + '|' + (peakDecay ? 'peak-decay' : 'normal');
      const group = groups.get(key);
      if (group) {
        group.count += 1;
        group.latest = Math.max(group.latest, signal.t);
        group.notionalUSD += finite(signal.alert_notional_usd) ? signal.alert_notional_usd : 0;
        continue;
      }
      groups.set(key, { bucket, kind, side, peakDecay, count: 1, first: signal.t, latest: signal.t,
        price: finite(signal.reference_price) && signal.reference_price > 0 ? signal.reference_price : 0,
        notionalUSD: finite(signal.alert_notional_usd) ? signal.alert_notional_usd : 0 });
    }
    return Array.from(groups.values()).sort((a, b) => a.bucket - b.bucket);
  }

  // filterAlerts keeps one entry per alert id and normalises the direction used
  // by the chart. An alert's trade_side is the direction shown on the alerts
  // page; older rows may omit it, so the liquidation side is the fallback.
  function filterAlerts(alerts) {
    const byId = new Map();
    for (const alert of alerts) {
      if (!alert || !alert.id || !Number.isFinite(alert.t)) continue;
      const side = alert.side === 'short' || alert.trade_side === 'short' ? 'short' : 'long';
      byId.set(alert.id, Object.assign({}, alert, { side }));
    }
    return Array.from(byId.values()).sort((a, b) => a.t - b.t);
  }

  // alertGroups keeps several alerts in the same candle readable while retaining
  // their direction, reference price and total liquidation amount for hover text.
  function alertGroups(alerts, step) {
    const groups = new Map();
    for (const alert of filterAlerts(alerts)) {
      const bucket = bucketStart(alert.t, step);
      const key = bucket + '|' + alert.side;
      const group = groups.get(key);
      if (group) {
        group.count += 1;
        group.latest = Math.max(group.latest, alert.t);
        group.notionalUSD += finite(alert.notional_usd) ? alert.notional_usd : 0;
        if (alert.reference_price > 0) group.price = alert.reference_price;
        if (alert.liquidation_side === 'short' || alert.liquidation_side === 'long') group.liquidationSide = alert.liquidation_side;
        continue;
      }
      groups.set(key, { type: 'alert', bucket, side: alert.side, count: 1, first: alert.t, latest: alert.t,
        price: finite(alert.reference_price) && alert.reference_price > 0 ? alert.reference_price : 0,
        liquidationSide: alert.liquidation_side || alert.side,
        notionalUSD: finite(alert.notional_usd) ? alert.notional_usd : 0 });
    }
    return Array.from(groups.values()).sort((a, b) => a.bucket - b.bucket);
  }

  // signalWindowStart reports the oldest signal time, which decides whether more
  // signal pages are needed to cover the visible chart range.
  function oldestSignal(signals) {
    return signals.reduce((oldest, signal) => Number.isFinite(signal.t) ? Math.min(oldest, signal.t) : oldest, Infinity);
  }

  const exported = { INTERVALS, INTERVAL_MS, DEFAULT_VISIBLE, MIN_VISIBLE, MAX_VISIBLE, RIGHT_PAD, HISTORY_TRIGGER,
    intervalMs, bucketStart, fitWindow, clampWindow, zoomWindow, panWindow, visibleCandles, priceRange, volumeMax,
    niceTicks, mergeCandles, normaliseCandles, filterSignals, markerGroups, filterAlerts, alertGroups, oldestSignal,
    radarZone, dragPriceRange, zoomPriceRange };
  if (typeof module === 'object' && module.exports) module.exports = exported;
  else root.LiquidationKline = exported;
})(typeof window === 'object' ? window : globalThis);
