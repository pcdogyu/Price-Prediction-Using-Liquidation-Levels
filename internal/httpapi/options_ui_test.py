"""Optional real-browser options check; --production checks authenticated live data."""
import copy
import datetime
import json
import os
import pathlib
import sys
from urllib.parse import parse_qs, urlsplit

sys.stdout.reconfigure(encoding="utf-8")
if os.getenv("TEMP"):
    sys.path.insert(0, os.path.join(os.environ["TEMP"], "codex-browser-validation-deps"))
from playwright.sync_api import Error, expect, sync_playwright

base = sys.argv[1].rstrip("/")
production = "--production" in sys.argv
root = pathlib.Path(__file__).resolve().parents[2]
artifacts = root / "build"
artifacts.mkdir(exist_ok=True)
errors, records, held = [], [], []

with sync_playwright() as playwright:
    launch = {"headless": True}
    chrome = pathlib.Path(r"C:\Program Files\Google\Chrome\Application\chrome.exe")
    if chrome.exists():
        launch["executable_path"] = str(chrome)
    browser = playwright.chromium.launch(**launch)
    context = browser.new_context(viewport={"width": 1440, "height": 1100})
    context.add_init_script("""
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

    response = context.request.get(base + "/api/v1/options")
    assert response.status == 200
    baseline = response.json()
    assert baseline["data"]["hours"] == 24
    assert [s["symbol"] for s in baseline["data"]["series"]] == ["BTCUSDT", "ETHUSDT"]
    state = {"failure": False, "custom": None, "hold": False}

    def fixture(route):
        hours = int(parse_qs(urlsplit(route.request.url).query)["hours"][0])
        records.append(hours)
        if state["hold"]:
            held.append(route)
            state["hold"] = False
            return
        if state["failure"]:
            route.fulfill(status=503, json={"detail": "fixture unavailable"})
            return
        packet = copy.deepcopy(state["custom"] or baseline)
        packet["data"]["hours"] = hours
        route.fulfill(status=200, json=packet)

    if not production:
        page.route("**/api/v1/options?**", fixture)

    def loaded():
        expect(page.locator("#options-status")).to_contain_text("已加载", timeout=30000)
        page.wait_for_function("() => window.__testActiveRequests === 0")
        expect(page.locator("#options-rows tr")).to_have_count(2)

    def refresh():
        page.locator("#options-refresh").click()
        loaded()

    if production:
        page.goto(base + "/market-info", wait_until="domcontentloaded")
        page.get_by_role("link", name="期权", exact=True).click()
    else:
        page.goto(base + "/options", wait_until="domcontentloaded")
    loaded()
    expect(page.locator('.app-links a[aria-current=page]')).to_have_text("期权")
    assert page.locator("#options-hours").input_value() == "24"
    expect(page.locator("#options-window")).to_contain_text("窗口 24 小时")
    rendered = int(page.locator("#options-chart").get_attribute("data-points"))
    expected = sum(len(s["points"]) for s in baseline["data"]["series"])
    assert rendered >= expected if production else rendered == expected
    assert page.evaluate("() => window.__testIntervals.some(item => item.ms === 10000)")
    if production:
        now = datetime.datetime.now(datetime.timezone.utc)
        for series in baseline["data"]["series"]:
            point = series["latest"]
            assert series["state"] == "ok", series
            assert point and point["contracts"] == point["selected_contracts"] == 80
            assert abs((point["call_gamma"] - point["put_gamma"]) / (point["call_gamma"] + point["put_gamma"]) - point["gamma"]) < 1e-12
            at = datetime.datetime.fromisoformat(point["time"].replace("Z", "+00:00"))
            assert (now - at).total_seconds() < 180
        print(json.dumps({"live": [{"symbol": s["symbol"], "gamma": s["latest"]["gamma"], "contracts": s["latest"]["contracts"], "time": s["latest"]["time"], "points": len(s["points"])} for s in baseline["data"]["series"]]}, ensure_ascii=False))
    else:
        expect(page.locator("#options-rows tr").nth(0)).to_contain_text("0.396667")
        expect(page.locator("#options-rows tr").nth(1)).to_contain_text("-0.396667")
        canvas = page.locator("#options-chart")
        canvas.hover(position={"x": 400, "y": 120})
        expect(page.locator("#options-tooltip")).to_contain_text("BTCUSDT")
        expect(page.locator("#options-tooltip")).to_contain_text("ETHUSDT")
        for hours in (1, 168):
            page.locator("#options-hours").fill(str(hours))
            page.locator("#options-hours").dispatch_event("change")
            loaded()
            expect(page.locator("#options-window")).to_contain_text(f"窗口 {hours} 小时")
        # Failed updates preserve both the chart and table; the button retries.
        before = page.locator("#options-rows").inner_text()
        state["failure"] = True
        page.locator("#options-refresh").click()
        expect(page.locator("#options-error")).to_contain_text("fixture unavailable")
        expect(page.locator("#options-status")).to_contain_text("保留上次")
        assert page.locator("#options-rows").inner_text() == before
        state["failure"] = False
        refresh()
        expect(page.locator("#options-error")).to_be_hidden()
        # An old, delayed window must not replace the latest selection.
        state["hold"] = True
        page.locator("#options-hours").fill("2")
        page.locator("#options-hours").dispatch_event("change")
        page.wait_for_function("() => document.querySelector('#options-status').textContent.includes('正在读取')")
        page.wait_for_timeout(100)
        assert held
        page.locator("#options-hours").fill("24")
        page.locator("#options-hours").dispatch_event("change")
        loaded()
        try:
            held.pop().fulfill(status=200, json=baseline)
        except Error:
            pass  # Aborted requests may already have been discarded by Chrome.
        expect(page.locator("#options-window")).to_contain_text("窗口 24 小时")
        custom = copy.deepcopy(baseline)
        custom["data"]["series"][0]["latest"]["gamma"] = 0
        custom["data"]["series"][0]["state"] = "partial"
        custom["data"]["series"][0]["last_error"] = "Deribit upstream unavailable"
        custom["data"]["series"][1]["latest"] = None
        custom["data"]["series"][1]["state"] = "unavailable"
        custom["data"]["series"][1]["points"] = []
        custom["data"]["series"][0]["points"][0]["gamma"] = 1
        custom["data"]["series"][0]["points"][1]["gamma"] = -1
        state["custom"] = custom
        refresh()
        expect(page.locator("#options-rows tr").nth(0)).to_contain_text("0.000000")
        expect(page.locator("#options-rows tr").nth(0)).to_contain_text("保留上次成功数据")
        expect(page.locator("#options-rows tr").nth(1)).to_contain_text("等待采集")
        expect(page.locator("#options-rows tr").nth(1).locator("td").nth(2)).to_have_text("—")
        assert "NaN" not in page.locator("main").inner_text()
        for series in custom["data"]["series"]:
            series["latest"] = None
            series["state"] = "unavailable"
            series["points"] = []
            series.pop("last_error", None)
        state["custom"] = custom
        refresh()
        expect(page.locator("#options-status")).to_contain_text("已加载 0 个点")
        expect(page.locator("#options-chart")).to_have_attribute("data-points", "0")
        expect(page.locator("#options-rows tr").nth(0).locator("td").nth(2)).to_have_text("—")
        state["custom"] = None
        refresh()

    # Both themes and narrow viewports must retain the legend, readable text, and contained table.
    mode = "live" if production else "fixture"
    for width in (1440, 390):
        page.set_viewport_size({"width": width, "height": 1100})
        for theme in ("dark", "light"):
            page.evaluate("theme => window.LiquidationTheme.set(theme)", theme)
            assert page.evaluate("() => document.documentElement.scrollWidth <= window.innerWidth")
            assert page.locator("#options-chart").bounding_box()["width"] <= width
            for name in ("BTCUSDT Deribit", "ETHUSDT Deribit", "0 轴"):
                expect(page.locator(".options-legend")).to_contain_text(name)
            page.screenshot(path=str(artifacts / f"options-{mode}-{width}-{theme}.png"), full_page=True)
    assert not errors, errors
    print(json.dumps({"status": "passed", "mode": mode, "screenshots": 4, "requests": records}, ensure_ascii=False))
    browser.close()
