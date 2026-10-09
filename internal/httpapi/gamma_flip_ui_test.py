"""Isolated Gamma Flip UI regression, or authenticated live verification with --production."""
import copy
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
errors, evidence = [], []

with sync_playwright() as playwright:
    launch = {"headless": True}
    chrome = pathlib.Path(r"C:\Program Files\Google\Chrome\Application\chrome.exe")
    if chrome.exists():
        launch["executable_path"] = str(chrome)
    browser = playwright.chromium.launch(**launch)
    context = browser.new_context(viewport={"width": 1440, "height": 1100})
    context.add_init_script("""
      window.setInterval = () => 1;
      window.__testActiveRequests = 0;
      const originalFetch = window.fetch;
      window.fetch = async (...args) => {
        window.__testActiveRequests++;
        try {
          const response = await originalFetch(...args), json = response.json.bind(response);
          response.json = async () => {try {return await json();} finally {window.__testActiveRequests--;}};
          return response;
        } catch (error) {window.__testActiveRequests--;throw error;}
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

    baseline = context.request.get(base + "/api/v1/market-info?symbol=ETHUSDT&range=1h").json()
    state = {"failure": False, "mode": "ok"}

    def fixture(route):
        if state["failure"]:
            route.fulfill(status=503, json={"detail": "fixture unavailable"})
            return
        query = parse_qs(urlsplit(route.request.url).query)
        symbol = query["symbol"][0]
        packet = copy.deepcopy(baseline)
        packet["symbol"] = symbol
        spot = 2500 if symbol == "ETHUSDT" else 100000
        flip = 2650.125 if symbol == "ETHUSDT" else 101000.125
        strikes = [spot * v for v in (.8, .9, 1, 1.1, 1.2)]
        packet["gamma"] = {
            **packet["gamma"], "symbol": symbol, "spot_price": spot,
            "state": "ok", "time": "2026-10-09T08:00:00Z", "contracts": 10, "expected_contracts": 10,
            "net_gex_usd": 100000, "absolute_gex_usd": 300000, "gamma_wall": strikes[2],
            "gamma_flip": flip, "gamma_flips": [spot * .88, flip], "flip_state": state["mode"],
            "flip_contracts": 10, "flip_expected_contracts": 10, "flip_range_low": spot * .5, "flip_range_high": spot * 1.5,
            "flip_method": "Gamma Flip：固定当前 IV，按 Black–Scholes 重估净 GEX，显示最近现价的零交叉。",
            "levels": [{"strike": strike, "net_gex_usd": (i-2)*100000, "absolute_gex_usd": abs(i-2)*100000, "call_oi": 10, "put_oi": 10} for i, strike in enumerate(strikes)],
            "expiries": [{"expiry": "2026-10-30", "contracts": 10, "net_gex_usd": 100000}], "warnings": [],
        }
        if state["mode"] in ("unavailable", "no_crossing"):
            packet["gamma"]["gamma_flip"] = None
            packet["gamma"]["gamma_flips"] = []
        if state["mode"] == "unavailable":
            packet["gamma"]["levels"] = []
            packet["gamma"]["state"] = "unavailable"
        if state["mode"] == "partial":
            packet["gamma"]["flip_contracts"] = 8
        route.fulfill(status=200, json=packet)

    if not production:
        page.route("**/api/v1/market-info?**", fixture)

    def loaded():
        page.wait_for_function("() => window.__testActiveRequests === 0 && document.querySelectorAll('#gamma-metrics .info-metric').length === 5")
        expect(page.locator("#page-error")).to_be_hidden()
        assert page.locator("#gamma-chart").bounding_box()["height"] == 338
        assert page.locator("#oi-chart").bounding_box()["height"] == 260

    def refresh():
        page.locator("#refresh").click()
        loaded()

    page.goto(base + "/market-info", wait_until="domcontentloaded")
    loaded()
    card = page.locator(".gamma-flip-metric")
    expect(card.locator(".label")).to_have_text("Gamma Flip 价格")
    if production:
        for symbol in ("ETHUSDT", "BTCUSDT"):
            page.locator("#symbol").select_option(symbol)
            loaded()
            data = context.request.get(base + "/api/v1/market-info?symbol=" + symbol + "&range=1h").json()["gamma"]
            assert data["flip_method"] and data["flip_contracts"] > 0
            assert data["state"] in ("ok", "partial")
            flip = data["gamma_flip"]
            if flip is not None:
                assert data["flip_range_low"] <= flip <= data["flip_range_high"]
                assert abs(flip-data["spot_price"]) == min(abs(value-data["spot_price"]) for value in data["gamma_flips"])
                text = page.evaluate("v => v.toLocaleString('zh-CN',{maximumFractionDigits:2})", flip)
                expect(card.locator(".number")).to_have_text(text)
                expect(page.locator("#gamma-chart")).to_have_attribute("data-flip-price", str(flip))
            else:
                expect(card.locator(".number")).to_have_text("—")
                expect(card).to_contain_text("无零交叉")
            evidence.append({"symbol": symbol, "height": 338, "flip": flip, "state": data["flip_state"], "roots": data["gamma_flips"], "coverage": [data["flip_contracts"], data["flip_expected_contracts"]]})
        page.locator("#symbol").select_option("ETHUSDT")
        loaded()
    else:
        expect(card.locator(".number")).to_have_text("2,650.13")
        expect(card).to_contain_text("2 处零交叉")
        expect(page.locator("#gamma-chart")).to_have_attribute("data-flip-price", "2650.125")
        page.locator("#symbol").select_option("BTCUSDT")
        loaded()
        expect(card.locator(".number")).to_have_text("101,000.13")
        for mode, text in (("partial", "部分期权链估算"), ("no_crossing", "无零交叉"), ("unavailable", "等待计算")):
            state["mode"] = mode
            refresh()
            expect(card).to_contain_text(text)
            if mode != "partial":
                expect(card.locator(".number")).to_have_text("—")
                assert page.locator("#gamma-chart").get_attribute("data-flip-price") is None
        state["mode"] = "ok"
        refresh()
        prior = card.inner_text()
        state["failure"] = True
        page.locator("#refresh").click()
        expect(page.locator("#page-error")).to_contain_text("fixture unavailable")
        assert card.inner_text() == prior
        state["failure"] = False
        page.locator("#symbol").select_option("ETHUSDT")
        loaded()

    mode = "live" if production else "fixture"
    for width in (1440, 390):
        page.set_viewport_size({"width": width, "height": 1100})
        for theme in ("dark", "light"):
            page.evaluate("theme => window.LiquidationTheme.set(theme)", theme)
            assert page.evaluate("() => document.documentElement.scrollWidth <= innerWidth")
            assert page.locator("#gamma-chart").bounding_box()["height"] == 338
            page.locator("#gamma-chart").locator("..").screenshot(path=str(artifacts / f"gamma-flip-{mode}-{width}-{theme}.png"))
    assert not errors, errors
    print(json.dumps({"status": "passed", "mode": mode, "screenshots": 4, "evidence": evidence}, ensure_ascii=False))
    browser.close()
