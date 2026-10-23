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
  const LIQUIDATION_DELTA_INTERVAL_MS = 10 * 60 * 1000, LIQUIDATION_DELTA_MAX_MS = 180 * 24 * 60 * 60 * 1000;
  const LIQUIDATION_DELTA_CUMULATIVE_HOURS = 48, LIQUIDATION_DELTA_CUMULATIVE_MS = LIQUIDATION_DELTA_CUMULATIVE_HOURS * 60 * 60 * 1000;
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

  // liquidationDeltaWindow converts the K-line viewport into the bounded
  // server request used by the lower chart. It follows the available 180-day
  // liquidation retention horizon and aligns both edges to complete 10-minute
  // buckets.
  function liquidationDeltaWindow(range, now, interval = LIQUIDATION_DELTA_INTERVAL_MS, maxRange = LIQUIDATION_DELTA_MAX_MS) {
    if (!range || !finite(range.start) || !finite(range.end) || range.end <= range.start || !finite(now) || interval <= 0 || maxRange <= 0) return null;
    const latest = bucketStart(now, interval) + interval;
    const oldest = latest - maxRange;
    const start = Math.max(oldest, bucketStart(range.start, interval));
    const requestedEnd = Math.min(latest, range.end);
    const end = Math.min(latest, Math.ceil(requestedEnd / interval) * interval);
    return end > start ? { start, end } : null;
  }

  // The cumulative baseline is independent of the K-line viewport. Panning or
  // zooming therefore changes which bars are visible, never their meaning.
  function liquidationDeltaCumulativeWindow(now, interval = LIQUIDATION_DELTA_INTERVAL_MS, hours = LIQUIDATION_DELTA_CUMULATIVE_HOURS) {
    if (!finite(now) || interval <= 0 || !finite(hours) || hours <= 0) return null;
    const latest = bucketStart(now, interval) + interval;
    return { start: latest - hours * 60 * 60 * 1000, end: latest };
  }

  function visibleLiquidationDelta(points, range, interval = LIQUIDATION_DELTA_INTERVAL_MS) {
    if (!range || !finite(range.start) || !finite(range.end) || range.end <= range.start) return [];
    return (points || []).filter(point => point && finite(point.t) && point.t + interval > range.start && point.t < range.end);
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

  // radarZones calculates the historical VWAP/Donchian three-way split for each
  // candle that has complete rolling windows. The server's latest zone is still
  // used by the app when available; these local values provide the historical
  // bands and use the same hard boundaries as the live radar.
  function radarZones(candles, periodMinutes, zThreshold, intervalMinutes, rangeMinutes, minDrawdown, donchianFraction) {
    const minutes = Number(intervalMinutes);
    const period = Number(periodMinutes);
    const threshold = Number(zThreshold);
    const range = Number(rangeMinutes) > 0 ? Number(rangeMinutes) : period;
    const drawdown = Number(minDrawdown) > 0 ? Number(minDrawdown) : .022;
    const fraction = Number(donchianFraction) > 0 ? Number(donchianFraction) : .35;
    if (!Array.isArray(candles) || !Number.isFinite(minutes) || minutes <= 0 || !Number.isFinite(period) || period <= 0 || !Number.isFinite(threshold) || threshold <= 0 || !Number.isFinite(range) || range <= 0 || !Number.isFinite(drawdown) || drawdown <= 0 || !Number.isFinite(fraction) || fraction <= 0 || fraction >= .5) return null;
    const bars = Math.ceil(period / minutes), rangeBars = Math.ceil(range / minutes), requiredBars = Math.max(bars, rangeBars);
    if (bars < 2 || rangeBars < 2 || candles.length < requiredBars) return null;
    const zones = [];
    for (let index = requiredBars - 1; index < candles.length; index += 1) {
      const window = candles.slice(index - bars + 1, index + 1);
      const rangeWindow = candles.slice(index - rangeBars + 1, index + 1);
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
      if (weight <= 0) continue;
      vwap /= weight;
      let variance = 0;
      for (const candle of window) {
        if (!finite(candle.close) || candle.close <= 0) continue;
        const rawVolume = finite(candle.volume) && candle.volume > 0 ? candle.volume : 0;
        const volume = volumeWeighted ? rawVolume : 1;
        variance += volume * (candle.close - vwap) * (candle.close - vwap);
      }
      const sigma = Math.sqrt(variance / weight);
      const price = candles[index].close;
      if (!finite(sigma) || sigma <= 0 || !finite(price) || price <= 0) continue;
      let high = -Infinity, low = Infinity, validRange = true;
      for (const candle of rangeWindow) {
        if (!finite(candle.high) || !finite(candle.low) || candle.high <= 0 || candle.low <= 0 || candle.high < candle.low) { validRange = false; break; }
        high = Math.max(high, candle.high);
        low = Math.min(low, candle.low);
      }
      if (!validRange || !finite(high) || !finite(low) || high < low) continue;
      const bounds = vwapZoneBounds(vwap, sigma, threshold, high, low, drawdown, fraction);
      const greenZoneUpper = bounds.green_zone_upper, redZoneLower = bounds.red_zone_lower;
      zones.push({ t: candles[index].t, ready: true, period_minutes: period, z_score_threshold: threshold,
        range_hours: range / 60, min_drawdown_pct: drawdown * 100, donchian_pct: fraction * 100,
        vwap, sigma, current_price: price, current_z_score: (price - vwap) / sigma,
        donchian_high: high, donchian_low: low, green_zone_upper: greenZoneUpper, red_zone_lower: redZoneLower,
        upper_open_price: redZoneLower, lower_open_price: greenZoneUpper, boundary_overlap: bounds.boundary_overlap });
    }
    return zones.length ? zones : null;
  }

  // radarZone mirrors the server's latest 1m VWAP/Donchian zone when the local
  // candle cache is the only available source.
  function radarZone(candles, periodMinutes, zThreshold, intervalMinutes, rangeMinutes, minDrawdown, donchianFraction) {
    const zones = radarZones(candles, periodMinutes, zThreshold, intervalMinutes, rangeMinutes, minDrawdown, donchianFraction);
    return zones && zones.length ? zones[zones.length - 1] : null;
  }

  function vwapZoneBounds(vwap, sigma, threshold, high, low, minDrawdown, donchianFraction) {
    const greenZoneUpper = Math.min(vwap - threshold * sigma, high * (1 - minDrawdown), low + donchianFraction * (high - low));
    const redZoneLower = Math.max(vwap + threshold * sigma, low * (1 + minDrawdown), high - donchianFraction * (high - low));
    return { green_zone_upper: greenZoneUpper, red_zone_lower: redZoneLower, boundary_overlap: greenZoneUpper >= redZoneLower };
  }

  // vwapSignalAllowed keeps only signals whose direction matches the opening
  // side of the historical VWAP/Donchian zone. Missing or overlapping opening
  // boundaries fail closed because the chart must not suggest an unsafe entry.
  function vwapSignalAllowed(signal, zone) {
    if (!signal || !zone || !finite(zone.red_zone_lower) || !finite(zone.green_zone_upper) || zone.boundary_overlap) return false;
    const price = finite(signal.reference_price) && signal.reference_price > 0 ? signal.reference_price : zone.current_price;
    if (!finite(price) || price <= 0) return false;
    if (signal.side === 'short') return price >= zone.red_zone_lower;
    if (signal.side === 'long') return price <= zone.green_zone_upper;
    return false;
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

  // normaliseLiquidationDelta keeps the chart tolerant of a partial response
  // while preserving both the signed bucket value and the cumulative value.
  function normaliseLiquidationDelta(rows) {
    const values = [];
    for (const row of rows || []) {
      const at = Date.parse(row && row.time);
      const longUSD = row && row.long_usd;
      const shortUSD = row && row.short_usd;
      if (!Number.isFinite(at) || !finite(longUSD) || longUSD < 0 || !finite(shortUSD) || shortUSD < 0) continue;
      const deltaUSD = finite(row.delta_usd) ? row.delta_usd : shortUSD - longUSD;
      if (!finite(deltaUSD)) continue;
      values.push({ time: new Date(at).toISOString(), t: at, long_usd: longUSD, short_usd: shortUSD, delta_usd: deltaUSD,
        cumulative_delta_usd: finite(row.cumulative_delta_usd) ? row.cumulative_delta_usd : NaN,
        count: Number.isInteger(row.count) && row.count >= 0 ? row.count : 0 });
    }
    values.sort((a, b) => a.t - b.t);
    let cumulative = 0;
    for (const point of values) {
      cumulative = finite(point.cumulative_delta_usd) ? point.cumulative_delta_usd : cumulative + point.delta_usd;
      point.cumulative_delta_usd = cumulative;
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
      const key = bucket + '|' + kind + '|' + side;
      const group = groups.get(key);
      if (group) {
        group.count += 1;
        group.latest = Math.max(group.latest, signal.t);
        group.notionalUSD += finite(signal.alert_notional_usd) ? signal.alert_notional_usd : 0;
        continue;
      }
      groups.set(key, { bucket, kind, side, count: 1, first: signal.t, latest: signal.t,
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
    LIQUIDATION_DELTA_INTERVAL_MS, LIQUIDATION_DELTA_MAX_MS, LIQUIDATION_DELTA_CUMULATIVE_HOURS, LIQUIDATION_DELTA_CUMULATIVE_MS,
    intervalMs, bucketStart, fitWindow, clampWindow, zoomWindow, panWindow, visibleCandles,
    liquidationDeltaWindow, liquidationDeltaCumulativeWindow, visibleLiquidationDelta, priceRange, volumeMax,
    niceTicks, mergeCandles, normaliseCandles, normaliseLiquidationDelta, filterSignals, markerGroups, filterAlerts, alertGroups, oldestSignal,
    radarZone, radarZones, vwapZoneBounds, vwapSignalAllowed, dragPriceRange, zoomPriceRange };
  if (typeof module === 'object' && module.exports) module.exports = exported;
  else root.LiquidationKline = exported;
})(typeof window === 'object' ? window : globalThis);
