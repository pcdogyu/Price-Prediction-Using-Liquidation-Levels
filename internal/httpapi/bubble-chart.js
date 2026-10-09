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

  function placements(events, range, bounds, x, y, offset = 6) {
    const points = [];
    let outside = 0;
    // Draw smaller circles last so they remain reachable; every event keeps its own circle.
    for (const event of events.slice().sort((a, b) => b.notional_usd - a.notional_usd)) {
      if (event.price < range[0] || event.price > range[1]) { outside++; continue; }
      const r = radius(event.notional_usd);
      const margin = r + 1; // Include the stroke inside the common plot clip.
      const cx = Math.max(bounds.left + margin, Math.min(bounds.right - margin, x(Date.parse(event.event_time))));
      const cy = Math.max(bounds.top + margin, Math.min(bounds.bottom - margin, y(event.price) + (event.position_side === 'long' ? offset : -offset)));
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
    async ensure(symbol, start, end, retry = false) {
      const key = symbol + ':' + start + ':' + end;
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
              const params = new URLSearchParams({ symbol, side: 'all', field: 'notional_usd', minimum: '0', limit: '500', from: new Date(from).toISOString(), to: new Date(to).toISOString() });
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

  const exported = { candleRange, mergeEvents, windowEvents, radius, placements, hitIndex, WindowLoader };
  if (typeof module === 'object' && module.exports) module.exports = exported;
  else root.BubbleChart = exported;
})(typeof window === 'object' ? window : globalThis);
