"""Optional isolated Playwright regression, or live read-only verification with --production."""
import copy
import datetime as dt
import json
import os
import pathlib
import sys
from urllib.parse import parse_qs, urlsplit

sys.stdout.reconfigure(encoding="utf-8")
if os.getenv("TEMP"):
    sys.path.insert(0, os.path.join(os.environ["TEMP"], "codex-browser-validation-deps"))
from playwright.sync_api import expect, sync_playwright

base = sys.argv[1].rstrip("/")
production = "--production" in sys.argv
root = pathlib.Path(__file__).resolve().parents[2]
artifacts = root / "build"
artifacts.mkdir(exist_ok=True)
intervals = {"1m": 60000, "2m": 120000, "3m": 180000, "5m": 300000, "10m": 600000, "15m": 900000, "30m": 1800000, "1h": 3600000, "4h": 14400000, "8h": 28800000, "12h": 43200000, "24h": 86400000}
errors, requests, evidence = [], [], []
iso = lambda ms: dt.datetime.fromtimestamp(ms / 1000, dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
ms = lambda text: dt.datetime.fromisoformat(text.replace("Z", "+00:00")).timestamp() * 1000

with sync_playwright() as playwright:
    options = {"headless": True}
    chrome = pathlib.Path(r"C:\Program Files\Google\Chrome\Application\chrome.exe")
    if chrome.exists():
        options["executable_path"] = str(chrome)
    browser = playwright.chromium.launch(**options)
    context = browser.new_context(viewport={"width": 1740, "height": 1150}, timezone_id="Asia/Shanghai")
    context.add_init_script("""
      localStorage.setItem('liquidation.interval', '1m');
      window.__testIntervals = [];
      window.setInterval = (fn, ms) => { window.__testIntervals.push({fn, ms}); return window.__testIntervals.length; };
      window.__testActiveRequests = 0;
      const originalFetch = window.fetch;
      window.fetch = async (...args) => {
        window.__testActiveRequests++;
        try {
          const response = await originalFetch(...args);
          const json = response.json.bind(response);
          response.json = async () => { try { return await json(); } finally { window.__testActiveRequests--; } };
          return response;
        } catch (error) { window.__testActiveRequests--; throw error; }
      };
      // Deterministic SSE delivery; the live pass checks persisted data without a moving window.
      window.EventSource = class {
        constructor(url) { this.listeners = {}; window.__testStream = this; }
        addEventListener(name, fn) { this.listeners[name] = fn; }
        emit(name, value) { this.listeners[name]?.({data: JSON.stringify(value)}); }
      };
    """)
    page = context.new_page()
    page.on("pageerror", lambda error: errors.append(str(error)))
    if production:
        entries = json.loads((root / "pem" / "password.json").read_text(encoding="utf-8"))
        entry = next(e for e in entries if e["username"] == "pcdog")
        page.goto(base + "/", wait_until="domcontentloaded")
        page.locator("input[name=username]").fill(entry["username"])
        page.locator("input[name=password]").fill(entry["password"])
        page.get_by_role("button", name="登录", exact=True).click()
        expect(page).to_have_url(base + "/bubbles")

    baseline = context.request.get(base + "/api/v1/market?symbol=ETHUSDT&interval=1m").json()
    history_base = context.request.get(base + "/api/v1/liquidations?symbol=ETHUSDT&limit=1").json()
    state = {"fail": False, "empty": False, "wall_factor": 1, "datasets": {}, "anchor": int(dt.datetime.now(dt.timezone.utc).timestamp() * 1000)}

    def dataset(symbol, interval, before=None):
        duration = intervals[interval]
        end = int(ms(before)) if before else state["anchor"] // duration * duration
        start = end - 120 * duration
        key = (symbol, interval, end)
        if key not in state["datasets"]:
            candles = [{"time": iso(start + i * duration), "open": 2490, "close": 2495, "low": 2484.59, "high": 2501.49, "volume_usd": 1000, "patterns": [], "complete": True} for i in range(120) if i != 10]
            rows = [{"id": f"{symbol}-{interval}-{end}-{i}", "exchange": "binance", "symbol": symbol, "position_side": "short" if i % 2 else "long", "price": 2490 + i % 5, "quantity": .001, "notional_usd": 2.49, "event_time": iso(start + int(i / 6003 * (119 * duration))), "coverage": "sampled"} for i in range(6003)]
            rows[0]["price"] = 2482.9  # At the padded lower border.
            rows[1]["price"] = 2800  # Counted as outside the price viewport.
            for i in (5999, 6000):
                rows[i].update(event_time=iso(start + 50 * duration + 20000), price=2492, position_side="long")
            rows.sort(key=lambda row: (row["event_time"], row["id"]))
            state["datasets"][key] = (candles, rows)
        return state["datasets"][key]

    def fixture(route):
        parsed = urlsplit(route.request.url)
        query = {k: v[0] for k, v in parse_qs(parsed.query).items()}
        requests.append((parsed.path, query))
        if parsed.path.endswith("/market"):
            symbol, interval = query["symbol"], query["interval"]
            state["interval"] = interval
            candles, rows = dataset(symbol, interval, query.get("before"))
            view = copy.deepcopy(baseline)
            view.update(symbol=symbol, candles=[] if state["empty"] else candles, liquidations=rows[-5000:], liquidation_minimum_usd=0, liquidations_truncated=True, realtime_price=None, model_signals=[], has_more=True, next_before=candles[0]["time"])
            view["summary"].update(last_price=2495, change_24h=0, change_pct_24h=0, updated_at=iso(state["anchor"]))
            route.fulfill(json=view)
        elif parsed.path.endswith("/liquidations"):
            if state["fail"]:
                route.fulfill(status=503, json={"detail": "fixture page unavailable"})
                return
            from_at, to_at = ms(query["from"]), ms(query["to"])
            rows_by_id = {}
            for (symbol, interval, _), (_, rows) in state["datasets"].items():
                if symbol == query["symbol"] and interval == state["interval"]:
                    for row in rows:
                        if from_at <= ms(row["event_time"]) < to_at:
                            rows_by_id[row["id"]] = row
            rows = sorted(rows_by_id.values(), key=lambda row: (row["event_time"], row["id"]), reverse=True)
            offset = int(query.get("cursor", 0))
            packet = copy.deepcopy(history_base)
            packet["data"].update(rows=rows[max(0, offset - 1):offset + 500], next_cursor=str(offset + 500) if offset + 500 < len(rows) else "")
            if not packet["data"]["next_cursor"]:
                packet["data"].pop("next_cursor")
            route.fulfill(json=packet)
        elif parsed.path.endswith("/map"):
            factor = state["wall_factor"]
            route.fulfill(json={"data_source": "coinglass_binance_liqmap", "bins": [{"price": 2232.9 / factor, "total_usd": 10000}, {"price": 2492, "total_usd": 20000}, {"price": 2749.5 * factor, "total_usd": 30000}], "bin_width": 1, "mark_price": 2495, "captured_at": iso(state["anchor"]), "top_long_liquidations": [{"price": 2232.9, "amount_usd": 10000}], "top_short_liquidations": [{"price": 2749.5, "amount_usd": 30000}]})
        elif parsed.path.endswith("/volume-profile"):
            route.fulfill(json={"state": "ok", "val": 100, "vah": 9000, "bins": [{"price_low": 100, "price_high": 200, "volume_usd": 10000}, {"price_low": 9000, "price_high": 9001, "volume_usd": 10000}]})
        elif parsed.path.endswith("/signals/latest"):
            route.fulfill(json={"state": "unavailable", "mark_price": 9000, "reason": "fixture"})
        else:
            route.continue_()

    if not production:
        page.route("**/api/v1/market?**", fixture)
        page.route("**/api/v1/liquidations?**", fixture)
        page.route("**/api/v1/map?**", fixture)
        page.route("**/api/v1/volume-profile?**", fixture)
        page.route("**/api/v1/signals/latest?**", fixture)

    def idle():
        page.wait_for_function("() => window.__testActiveRequests === 0")
        page.wait_for_function("() => document.getElementById('chart-context').textContent.includes('窗口已补齐') || document.getElementById('chart-context').textContent.includes('补齐失败') || document.getElementById('chart-context').textContent.includes('等待有效K线')")

    def refresh():
        page.evaluate("() => window.__testIntervals.find(item => item.ms === 10000).fn()")
        idle()

    def snapshot():
        return page.evaluate("() => ({low: Number(document.getElementById('market-chart').dataset.priceLow), high: Number(document.getElementById('market-chart').dataset.priceHigh), start: document.getElementById('market-chart').dataset.windowStart, end: document.getElementById('market-chart').dataset.windowEnd, context: document.getElementById('chart-context').textContent, ids: Array.from(document.querySelectorAll('[data-event-id]'), e => e.dataset.eventId), candles: visibleCandles().map(c => ({low:c.low, high:c.high}))})")

    def check_axis():
        view = snapshot()
        low, high = min(c["low"] for c in view["candles"]), max(c["high"] for c in view["candles"])
        if low == high:
            low, high = low - low * .0005, high + high * .0005
        padding = (high - low) * .1
        assert abs(view["low"] - (low - padding)) < 1e-6, view
        assert abs(view["high"] - (high + padding)) < 1e-6, view
        return view

    def drag(dx, dy=0):
        box = page.locator("#market-chart").bounding_box()
        x, y = box["x"] + box["width"] * .32, box["y"] + box["height"] * .5
        page.mouse.move(x, y)
        page.mouse.down()
        page.mouse.move(x + dx, y + dy, steps=4)
        page.mouse.up()
        idle()

    page.goto(base + "/bubbles", wait_until="domcontentloaded")
    idle()
    initial = check_axis()
    if not production:
        assert len(initial["ids"]) == 6002 and "窗口已加载 6003笔" in initial["context"] and "价格视野外 1笔" in initial["context"], initial["context"]
        all_rows = dataset("ETHUSDT", "1m")[1]
        assert set(initial["ids"]) == {r["id"] for r in all_rows if r["price"] != 2800}
        assert "视野外" in page.locator("#liquidation-top3").inner_text()
        assert "上方视野外" in page.locator("#market-chart").text_content() and "下方视野外" in page.locator("#market-chart").text_content()
        assert page.evaluate("() => Array.from(document.querySelectorAll('[data-event-id]')).every(e => Number(e.getAttribute('cx'))-Number(e.getAttribute('r'))>=72 && Number(e.getAttribute('cx'))+Number(e.getAttribute('r'))<=1025 && Number(e.getAttribute('cy'))-Number(e.getAttribute('r'))>=24 && Number(e.getAttribute('cy'))+Number(e.getAttribute('r'))<=518)")
        overlap_id = next(r["id"] for r in all_rows if r["id"].endswith("-6000"))
        circle = page.locator(f'[data-event-id="{overlap_id}"]')
        box = circle.bounding_box()
        page.mouse.move(box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
        expect(page.locator("#tooltip")).to_contain_text("重叠")
        page.mouse.move(5, 5)
        history_requests = sum(path.endswith("/liquidations") for path, _ in requests)
        state["wall_factor"] = 10
        refresh()
        assert check_axis()["low"] == initial["low"]
        # Only a tiny new tail may be calibrated, never repeat the full completed window.
        new_pages = [q for path, q in requests if path.endswith("/liquidations")][history_requests:]
        assert all(ms(q["from"]) > ms(initial["end"]) - 60000 for q in new_pages)
        tiny = copy.deepcopy(all_rows[50]); tiny["id"] = "sse-tiny"; tiny["notional_usd"] = .01
        page.evaluate("row => window.__testStream.emit('liquidation', row)", tiny)
        assert "sse-tiny" in snapshot()["ids"]
        page.evaluate("row => window.__testStream.emit('liquidation', row)", tiny)
        assert snapshot()["ids"].count("sse-tiny") == 1
        # A reconnect forces REST calibration while retaining the view.
        count = sum(path.endswith("/market") for path, _ in requests)
        page.evaluate("() => { window.__testStream.onerror(); window.__testStream.emit('health', {}); }")
        idle()
        assert sum(path.endswith("/market") for path, _ in requests) == count + 1
        tick = {"symbol": "ETHUSDT", "trade_id": 999999, "time": iso(ms(initial["end"]) - 1000), "price": 2505}
        page.evaluate("tick => window.__testStream.emit('price', tick)", tick)
        assert check_axis()["high"] > initial["high"]

    box = page.locator("#market-chart").bounding_box()
    page.mouse.move(box["x"] + box["width"] * .3, box["y"] + box["height"] * .5)
    page.mouse.wheel(0, -200)
    manual = snapshot()
    refresh()
    assert snapshot()["low"] == manual["low"] and snapshot()["high"] == manual["high"]
    page.locator("#chart-reset").click()
    check_axis()
    if not production:
        drag(0, 320)
        assert not snapshot()["ids"] and "价格视野外" in snapshot()["context"]
        page.locator("#chart-reset").click()
        check_axis()
    drag(140)
    if page.evaluate("() => chart.atLatest"):
        drag(140)
    historical = snapshot()
    assert "历史K线" in historical["context"]
    refresh()
    assert snapshot()["start"] == historical["start"] and snapshot()["end"] == historical["end"]
    if production:
        for symbol in ("ETHUSDT", "BTCUSDT"):
            page.locator(f'button[data-symbol="{symbol}"]').click()
            idle()
            check_axis()
            drag(140)
            if page.evaluate("() => chart.atLatest"):
                drag(140)
            view = snapshot()
            params = {"symbol": symbol, "side": "all", "field": "notional_usd", "minimum": "0", "limit": "500", "from": view["start"], "to": view["end"]}
            rows = {}
            while True:
                packet = context.request.get(base + "/api/v1/liquidations", params=params).json()["data"]
                rows.update((row["id"], row) for row in packet["rows"])
                if not packet.get("next_cursor"):
                    break
                params["cursor"] = packet["next_cursor"]
            drawn = {id for id, row in rows.items() if view["low"] <= row["price"] <= view["high"]}
            assert set(view["ids"]) == drawn, {"symbol": symbol, "missing": list(drawn - set(view["ids"])), "extra": list(set(view["ids"]) - drawn)}
            assert f"窗口已加载 {len(rows)}笔" in view["context"]
            evidence.append({"symbol": symbol, "start": view["start"], "end": view["end"], "history": len(rows), "bubbles": len(view["ids"]), "range": [view["low"], view["high"]]})
    page.locator("#chart-latest").click()
    idle()
    check_axis()
    for symbol in ("BTCUSDT", "ETHUSDT"):
        page.locator(f'button[data-symbol="{symbol}"]').click()
        idle()
        for interval in intervals:
            page.locator(f'button[data-interval="{interval}"]').click()
            idle()
            view = check_axis()
            assert page.locator(f'button[data-interval="{interval}"]').evaluate("e => e.classList.contains('active')")
            if not production:
                assert len(view["ids"]) == 6002, (symbol, interval, view["context"])
                assert all(id.startswith(symbol + "-" + interval + "-") for id in view["ids"])
            (artifacts / "bubble-ui-progress.json").write_text(json.dumps({"production": production, "symbol": symbol, "interval": interval, "bubbles": len(view["ids"])}), encoding="utf-8")
    if not production:
        state["fail"] = True
        page.locator('button[data-interval="1m"]').click()
        idle()
        expect(page.locator("#bubble-retry")).to_be_visible()
        expect(page.locator("#chart-context")).to_contain_text("补齐失败")
        state["fail"] = False
        page.locator("#bubble-retry").click()
        idle()
        assert len(snapshot()["ids"]) == 6002
        state["empty"] = True
        page.locator('button[data-interval="2m"]').click()
        idle()
        expect(page.locator("#market-chart")).to_contain_text("等待有效 K 线")
        state["empty"] = False
    page.locator('button[data-symbol="ETHUSDT"]').click()
    idle()
    page.locator('button[data-interval="1m"]').click()
    idle()
    for width in (1740, 390):
        page.set_viewport_size({"width": width, "height": 1100})
        for theme in ("light", "dark"):
            page.evaluate("theme => window.LiquidationTheme.set(theme)", theme)
            assert page.evaluate("() => document.documentElement.scrollWidth <= innerWidth + 1"), (width, theme)
            # Away from the edge clamp, actual screen displacement remains six pixels.
            assert page.evaluate("""() => {
              const svg = document.getElementById('market-chart'), rect = svg.getBoundingClientRect();
              const low = Number(svg.dataset.priceLow), high = Number(svg.dataset.priceHigh);
              const circles = Array.from(svg.querySelectorAll('[data-event-id]'));
              return circles.filter(e => { const y = 24 + (high-Number(e.dataset.eventPrice))/(high-low)*494; return y>50 && y<490; }).every(e => {
                const anchor = 24 + (high-Number(e.dataset.eventPrice))/(high-low)*494;
                return Math.abs(Math.abs(Number(e.getAttribute('cy'))-anchor)*rect.height/560 - 6) < .001;
              });
            }"""), (width, theme)
            assert page.evaluate("() => Array.from(document.querySelectorAll('[data-event-id]')).every(e => [getComputedStyle(document.documentElement).getPropertyValue('--buy').trim(),getComputedStyle(document.documentElement).getPropertyValue('--sell').trim()].includes(e.getAttribute('stroke')))")
            page.screenshot(path=str(artifacts / f"bubbles-{'production' if production else 'fixture'}-{width}-{theme}.png"), full_page=True)
            assert page.locator("#chart-context").is_visible()
    assert not errors, errors
    browser.close()
print(json.dumps({"production": production, "checked_symbols": 2, "checked_intervals": len(intervals), "fixture_events": 6003 if not production else None, "live_windows": evidence, "page_errors": errors}, ensure_ascii=False))
