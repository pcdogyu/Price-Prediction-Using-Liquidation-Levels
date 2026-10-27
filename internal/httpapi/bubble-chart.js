(function (root) {
  'use strict';
  const finite = value => typeof value === 'number' && Number.isFinite(value);

  function candleRange(candles) {
    let low = Infinity, high = -Infinity;
    for (const candle of candles) {
      if (!finite(candle.low) || !finite(candle.high) || candle.low <= 0 || candle.high < candle.low) continue;
      low = Math.min(low, candle.low);
      high = Math.max(high, candle.high);
    }
    if (!Number.isFinite(low)) return null;
    if (high === low) {
      const span = low * .001;
      low -= span / 2;
      high += span / 2;
    }
    const padding = (high - low) * .1;
    return [low - padding, high + padding];
  }

  function normalizeMarketSymbol(value) {
    let raw = String(value || '').trim().toUpperCase();
    if (!raw) return '';
    const colon = raw.lastIndexOf(':');
    if (colon >= 0) raw = raw.slice(colon + 1);
    raw = raw.replace(/\.(P|PERP)$/, '').replace(/[-_]?(PERP|SWAP)$/, '').replace(/[-_/]+/g, '');
    return /^[A-Z0-9]+USDT$/.test(raw) && raw.length > 4 ? raw : '';
  }

  function tradeTime(value) {
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value > 1e11 ? value : value * 1000;
    }
    const raw = String(value || '').trim();
    if (!raw) return NaN;
    if (raw.startsWith('0001-01-01')) return NaN;
    if (/^-?\d+(?:\.\d+)?$/.test(raw)) {
      const numeric = Number(raw);
      if (Number.isFinite(numeric)) return numeric > 1e11 ? numeric : numeric * 1000;
    }
    const parsed = Date.parse(raw);
    return Number.isFinite(parsed) ? parsed : NaN;
  }

  function normalizeTradeMarkers(rows, symbol, selectedKey = '') {
    const target = normalizeMarketSymbol(symbol);
    if (!target || !Array.isArray(rows)) return [];
    const markers = [];
    rows.forEach((row, index) => {
      if (!row || normalizeMarketSymbol(row.inst_id || row.symbol) !== target) return;
      const side = String(row.side || '').trim().toLowerCase();
      if (side !== 'long' && side !== 'short') return;
      const rowKey = String(row.row_key || row.key || index);
      const selected = rowKey === String(selectedKey || '') || row.selected === true;
      const add = (phase, timeValue, priceValue) => {
        const time = tradeTime(timeValue);
        const price = Number(priceValue);
        if (!Number.isFinite(time) || !Number.isFinite(price) || price <= 0) return;
        const isEntry = phase === 'entry';
        const action = side === 'long' ? (isEntry ? 'buy' : 'sell') : (isEntry ? 'sell' : 'buy');
        const orderID = String(isEntry ? (row.ord_id || row.entry_ord_id) : (row.ord_id || row.exit_ord_id) || '').trim();
        const rowOperation = String(row.operation || '').trim().toLowerCase();
        const operation = isEntry ? 'open' : (rowOperation || 'close');
        markers.push({
          id: rowKey + ':' + phase,
          row_key: rowKey,
          symbol: target,
          exchange: String(row.exchange || '').trim().toLowerCase(),
          side,
          phase,
          action,
          operation,
          operation_source: String(row.operation_source || '').trim().toLowerCase(),
          time,
          time_iso: new Date(time).toISOString(),
          price,
          selected,
          order_id: orderID,
          trade_id: String(row.trade_id || '').trim(),
          quantity: String(row.fill_sz || row.qty || '').trim(),
          turnover: String(row.turnover || '').trim(),
          notional_usdt: String(row.notional_usdt || row.notional || row.turnover || '').trim(),
          margin_usdt: String(row.margin_usdt || row.margin || '').trim(),
          fill_pnl: String(row.fill_pnl || '').trim(),
          net_pnl: String(row.net_pnl || '').trim(),
          fee: String(row.fee || '').trim(),
          fee_ccy: String(row.fee_ccy || '').trim()
        });
      };
      add('entry', row.entry_time, row.entry_px);
      add('exit', row.exit_time, row.exit_px);
    });
    return markers.sort((a, b) => a.time - b.time || a.id.localeCompare(b.id));
  }

  // A single candle can contain several fills for the same opening or closing
  // action. Keep one marker per phase and direction in that candle so the chart
  // describes the action without covering the candle with repeated labels.
  function collapseTradeMarkers(markers, intervalMs) {
    const step = Number(intervalMs);
    if (!Array.isArray(markers) || !Number.isFinite(step) || step <= 0) return Array.isArray(markers) ? markers.slice() : [];
    const result = [], groups = new Map();
    for (const marker of markers) {
      if (!marker || (marker.phase !== 'entry' && marker.phase !== 'exit') || !Number.isFinite(marker.time) || (marker.side !== 'long' && marker.side !== 'short')) {
        result.push(marker);
        continue;
      }
      const bucket = Math.floor(marker.time / step) * step;
      const key = bucket + ':' + marker.phase + ':' + marker.side;
      const existing = groups.get(key);
      if (!existing) {
        const copy = Object.assign({}, marker, { collapsed_count: 1 });
        groups.set(key, copy);
        result.push(copy);
        continue;
      }
      existing.collapsed_count += 1;
      existing.selected = existing.selected || marker.selected === true;
      if (existing.operation_source !== marker.operation_source) existing.operation_source = '';
      if (existing.phase === 'exit' && existing.operation !== marker.operation) existing.operation = 'close';
    }
    return result.sort((a, b) => a.time - b.time || String(a.id || '').localeCompare(String(b.id || '')));
  }

  function tradeMarkerPlacements(markers, range, scale, y, bounds) {
    let outside = 0;
    const placed = [];
    const clamp = (value, low, high) => Math.max(low, Math.min(high, value));
    const overlaps = (a, b) => a.left < b.right + 3 && a.right > b.left - 3 && a.top < b.bottom + 3 && a.bottom > b.top - 3;
    (Array.isArray(markers) ? markers : []).forEach(marker => {
      if (marker.time < scale.start || marker.time >= scale.end || marker.price < range[0] || marker.price > range[1]) {
        outside++;
        return;
      }
      const x = clamp(scale.x(marker.time), bounds.left + 9, bounds.right - 9);
      const priceY = clamp(y(marker.price), bounds.top + 9, bounds.bottom - 9);
      const long = marker.side === 'long';
      const closing = marker.phase === 'exit';
      // Opening markers use the compact trading-chart convention requested by
      // the position view: B for long below the candle, S for short above it.
      // Closing markers use the compact direction labels requested by the
      // position view; detailed operation/source data remains in the tooltip.
      const collapsedLabel = closing && marker.collapsed_count > 1 ? '×' + marker.collapsed_count : '';
      const labelText = closing ? (long ? '平多' : '平空') + collapsedLabel : (long ? 'B' : 'S');
      const labelWidth = labelText.length * 11 + 12;
      const labelBelow = closing ? !long : long;
      const direction = labelBelow ? 1 : -1;
      // Keep every label over its own candle. When a marker is close to the
      // edge, only clamp it enough to keep the full badge inside the plot.
      const labelX = clamp(x, bounds.left + labelWidth / 2 + 3, bounds.right - labelWidth / 2 - 3);
      const maxLevels = Math.ceil((bounds.bottom - bounds.top) / 24) + 2;
      let chosen = null;
      let fallback = null;
      let fallbackScore = Infinity;

      for (let level = 0; level < maxLevels; level++) {
        const distance = 25 + level * 24;
        const labelCenterY = clamp(priceY + direction * distance, bounds.top + 12, bounds.bottom - 12);
        const box = {
          left: labelX - labelWidth / 2 - 3,
          right: labelX + labelWidth / 2 + 3,
          top: labelCenterY - 12,
          bottom: labelCenterY + 12
        };
        const collisionCount = placed.reduce((count, prior) => count + (overlaps(box, prior.box) ? 1 : 0), 0);
        const score = collisionCount * 100000 + level * 100;
        if (score < fallbackScore) {
          fallbackScore = score;
          fallback = { labelX, labelCenterY, box };
        }
        if (collisionCount === 0) {
          chosen = { labelX, labelCenterY, box };
          break;
        }
      }

      const layout = chosen || fallback;
      placed.push({ marker, x, priceY, labelX: layout.labelX, labelCenterY: layout.labelCenterY, labelBelow, labelText, labelWidth, box: layout.box });
    });
    return { visible: placed.length, outside, markers: placed };
  }

  function mergeEvents(current, incoming, symbol) {
    const values = new Map();
    for (const event of [...current, ...incoming]) {
      if (!event?.id || event.exchange !== 'binance' || event.symbol !== symbol ||
          !finite(event.notional_usd) || event.notional_usd < 0 || !finite(event.price) || event.price <= 0 ||
          !['long', 'short'].includes(event.position_side) || !Number.isFinite(Date.parse(event.event_time))) continue;
      values.set(event.id, event);
    }
    return Array.from(values.values()).sort((a, b) => Date.parse(a.event_time) - Date.parse(b.event_time) || a.id.localeCompare(b.id));
  }

  function windowEvents(events, start, end) {
    return events.filter(event => { const at = Date.parse(event.event_time); return at >= start && at < end; });
  }

  function radius(amount) {
    return Math.max(3, Math.min(18, 3 + Math.log10(Math.max(100, amount) / 100) * 2.5));
  }

  function placementOffsets(value) {
    if (typeof value === 'number') {
      const amount = Number.isFinite(value) ? Math.max(0, value) : 0;
      return { long: amount, short: amount };
    }
    return {
      long: Number.isFinite(Number(value?.long)) ? Math.max(0, Number(value.long)) : 0,
      short: Number.isFinite(Number(value?.short)) ? Math.max(0, Number(value.short)) : 0
    };
  }

  function placements(events, range, bounds, x, y, offsets = { long: 20, short: 20 }) {
    const points = [];
    let outside = 0;
    const placementOffset = placementOffsets(offsets);
    // Draw smaller circles last so they remain reachable; every event keeps its own circle.
    for (const event of events.slice().sort((a, b) => b.notional_usd - a.notional_usd)) {
      if (event.price < range[0] || event.price > range[1]) { outside++; continue; }
      const r = radius(event.notional_usd);
      const margin = r + 1; // Include the stroke inside the common plot clip.
      const cx = Math.max(bounds.left + margin, Math.min(bounds.right - margin, x(Date.parse(event.event_time))));
      // SVG's y-axis grows downward: move long-liquidation bubbles down and
      // short-liquidation bubbles up by their independently configured offsets.
      const offset = event.position_side === 'long' ? placementOffset.long : -placementOffset.short;
      const cy = Math.max(bounds.top + margin, Math.min(bounds.bottom - margin, y(event.price) + offset));
      points.push({ event, x: cx, y: cy, radius: r });
    }
    return { points, outside };
  }

  function hitIndex(points) {
    const size = 40, cells = new Map();
    const key = (x, y) => x + ':' + y;
    for (const point of points) {
      const cell = key(Math.floor(point.x / size), Math.floor(point.y / size));
      if (!cells.has(cell)) cells.set(cell, []);
      cells.get(cell).push(point);
    }
    return (x, y) => {
      const found = [], cx = Math.floor(x / size), cy = Math.floor(y / size);
      for (let dx = -1; dx <= 1; dx++) for (let dy = -1; dy <= 1; dy++) {
        for (const point of cells.get(key(cx + dx, cy + dy)) || []) {
          if (Math.hypot(point.x - x, point.y - y) <= point.radius + 1) found.push(point.event);
        }
      }
      return found;
    };
  }

  function timeGridTicks(start, end, step) {
    const low = Number(start), high = Number(end), interval = Number(step);
    if (!Number.isFinite(low) || !Number.isFinite(high) || !(high > low) || !Number.isFinite(interval) || interval <= 0) return [];
    const ticks = [];
    const first = Math.ceil(low / interval) * interval;
    for (let at = first; at < high; at += interval) {
      ticks.push(at);
      if (ticks.length >= 10000) break;
    }
    return ticks;
  }

  function union(ranges, start, end) {
    if (!(end > start)) return ranges;
    const result = [];
    for (const range of [...ranges, [start, end]].sort((a, b) => a[0] - b[0])) {
      const last = result[result.length - 1];
      if (last && range[0] <= last[1]) last[1] = Math.max(last[1], range[1]);
      else result.push(range.slice());
    }
    return result;
  }

  function gaps(ranges, start, end) {
    const result = [];
    let at = start;
    for (const [low, high] of ranges) {
      if (high <= at) continue;
      if (low >= end) break;
      if (low > at) result.push([at, Math.min(low, end)]);
      at = Math.max(at, high);
    }
    if (at < end) result.push([at, end]);
    return result;
  }

  class WindowLoader {
    constructor(request, onRows, onState, now = Date.now) {
      this.request = request;
      this.onRows = onRows;
      this.onState = onState;
      this.now = now;
      this.ranges = [];
      this.epoch = 0;
      this.active = null;
      this.state = { status: 'waiting', pages: 0, loaded: 0 };
    }
    reset() {
      this.epoch++;
      this.active?.controller.abort();
      this.active = null;
      this.ranges = [];
      this.state = { status: 'waiting', pages: 0, loaded: 0 };
    }
    cover(start, end) { this.ranges = union(this.ranges, start, end); }
    async ensure(symbol, start, end, retry = false, minimum = 0) {
      const minimumUSD = Number.isFinite(Number(minimum)) && Number(minimum) >= 0 ? Number(minimum) : 0;
      const key = symbol + ':' + start + ':' + end + ':' + minimumUSD;
      if (this.active?.key === key) return this.active.promise;
      if (this.state.status === 'error' && this.state.key === key && !retry) return;
      this.active?.controller.abort();
      const epoch = ++this.epoch;
      const cutoff = Math.min(end, this.now());
      const missing = gaps(this.ranges, start, cutoff);
      const controller = new AbortController();
      const active = { key, controller };
      this.active = active;
      const valid = () => epoch === this.epoch && !controller.signal.aborted;
      const report = state => { if (valid()) { this.state = { key, cutoff, ...state }; this.onState(this.state); } };
      active.promise = (async () => {
        let pages = 0;
        const ids = new Set();
        try {
          if (missing.length) report({ status: 'loading', pages, loaded: 0 });
          for (const [from, to] of missing) {
            let cursor = '';
            const cursors = new Set();
            do {
              const params = new URLSearchParams({ symbol, side: 'all', field: 'notional_usd', minimum: String(minimumUSD), limit: '500', from: new Date(from).toISOString(), to: new Date(to).toISOString() });
              if (cursor) params.set('cursor', cursor);
              const packet = await this.request('liquidations?' + params, { signal: controller.signal });
              if (!valid()) return;
              if (!Array.isArray(packet?.data?.rows)) throw new Error('清算分页响应无效');
              const rows = windowEvents(mergeEvents([], packet.data.rows, symbol), from, to);
              rows.forEach(row => ids.add(row.id));
              this.onRows(rows);
              pages++;
              report({ status: 'loading', pages, loaded: ids.size });
              cursor = packet.data.next_cursor || '';
              if (cursor && cursors.has(cursor)) throw new Error('清算分页游标未前进');
              if (cursor) cursors.add(cursor);
            } while (cursor);
            this.cover(from, to);
          }
          report({ status: 'complete', pages, loaded: ids.size });
        } catch (error) {
          if (valid()) report({ status: 'error', pages, loaded: ids.size, error: error.message });
        } finally {
          if (this.active === active) this.active = null;
        }
      })();
      return active.promise;
    }
  }

  const exported = { candleRange, normalizeMarketSymbol, tradeTime, normalizeTradeMarkers, collapseTradeMarkers, tradeMarkerPlacements, mergeEvents, windowEvents, radius, placements, hitIndex, timeGridTicks, WindowLoader };
  if (typeof module === 'object' && module.exports) module.exports = exported;
  else root.BubbleChart = exported;
})(typeof window === 'object' ? window : globalThis);
