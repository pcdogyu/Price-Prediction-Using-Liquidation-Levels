"""Optional isolated browser regression: DASHBOARD_UI_SCRIPT points here.

Also accepts https://tvbot.lmitis.com/liquidation --production for live checks.
Credentials are read locally and never included in output or screenshots.
"""
import copy
import itertools
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
labels = ["1H", "4H", "12H", "24H"]
records = []
errors = []

with sync_playwright() as playwright:
    options = {"headless": True}
    chrome = pathlib.Path(r"C:\Program Files\Google\Chrome\Application\chrome.exe")
    if chrome.exists():
        options["executable_path"] = str(chrome)
    browser = playwright.chromium.launch(**options)
    context = browser.new_context(viewport={"width": 1440, "height": 1100})
    # Capture the scheduled refresh so tests can trigger it without racing fixtures.
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

    baseline = context.request.get(base + "/api/v1/liquidations?symbol=ALL&limit=1").json()
    state = {"pattern": "空空多多", "failure": False, "detail_failure": False, "rows_tag": "initial", "custom": None}

    def fixture(route):
        query = {k: v[0] for k, v in parse_qs(urlsplit(route.request.url).query, keep_blank_values=True).items()}
        records.append(query)
        summary = query["limit"] == "1"
        if (summary and state["failure"]) or (not summary and state["detail_failure"]):
            route.fulfill(status=503, json={"detail": "fixture unavailable"})
            return
        packet = copy.deepcopy(baseline)
        if summary:
            packet["data"]["periods"] = state["custom"] if state["custom"] is not None else [
                {"label": label, "long_usd": 200 if side == "多" else 100, "short_usd": 200 if side == "空" else 100, "count": 3}
                for label, side in zip(labels, state["pattern"])
            ]
        else:
            symbol = query["symbol"] if query["symbol"] != "ALL" else "SOLUSDT"
            packet["data"]["symbols"] = ["BTCUSDT", "ETHUSDT", "SOLUSDT"]
            packet["data"]["rows"] = [{"id": state["rows_tag"], "symbol": symbol, "position_side": "short" if query["side"] == "short" else "long", "event_time": query["to"], "price": 321 if state["rows_tag"] == "new-detail" else 123, "quantity": 456, "notional_usd": 56088}]
            packet["data"]["next_cursor"] = "fixture-next" if not query["cursor"] else ""
        route.fulfill(status=200, json=packet)

    if not production:
        page.route("**/api/v1/liquidations?**", fixture)
    else:
        def capture(request):
            if "/api/v1/liquidations?" in request.url:
                records.append({k: v[0] for k, v in parse_qs(urlsplit(request.url).query, keep_blank_values=True).items()})
        page.on("request", capture)

    def loaded():
        expect(page.locator("#liq-analysis-status")).to_contain_text("统计截止", timeout=30000)
        expect(page.locator("#liq-periods .info-metric")).to_have_count(4)

    def refresh():
        page.locator("#refresh").click()
        page.wait_for_function("() => window.__testActiveRequests === 0")
        page.wait_for_load_state("networkidle")

    def check_queries():
        summary = next(q for q in reversed(records) if q["limit"] == "1")
        detail = next(q for q in reversed(records) if q["limit"] == "50")
        assert all(summary[k] == v for k, v in {"symbol": "ETHUSDT", "side": "all", "field": "notional_usd", "minimum": "0"}.items()), summary
        assert "cursor" not in summary
        assert summary["to"] == detail["to"], (summary, detail)
        return summary, detail

    page.goto(base + "/liquidations", wait_until="networkidle")
    loaded()
    expect(page.locator("#liq-analysis-heading")).to_have_text("ETHUSDT 四周期清算结构")
    expect(page.locator("#liq-state-table-heading")).to_have_text("四周期 16 种状态行情解析")
    expect(page.locator("#liq-state-rows tr")).to_have_count(16)
    assert page.locator("#liq-periods .label").all_text_contents() == ["ETHUSDT · " + label + " 清算金额 USD" for label in labels]
    _, initial_detail = check_queries()
    assert initial_detail["symbol"] == "ETHUSDT"

    if not production:
        for parts in itertools.product("多空", repeat=4):
            pattern = "".join(parts)
            state["pattern"] = pattern
            refresh()
            expect(page.locator("#liq-combination")).to_have_text(pattern)
            expect(page.locator("#liq-state-rows tr.current .liq-table-combination")).to_have_text(pattern)
            expect(page.locator("#liq-state-rows tr.current .liq-current-label")).to_have_text("当前")
            assert page.locator("#liq-periods .liq-period-state").all_text_contents() == ["多头清算占优" if side == "多" else "空头清算占优" for side in pattern]
            assert "NaN" not in page.locator("#liq-periods").inner_text()
        state["custom"] = [
            {"label": "1H", "long_usd": 100, "short_usd": 100, "count": 2},
            {"label": "4H", "long_usd": 0, "short_usd": 0, "count": 0},
            {"label": "12H", "long_usd": 100, "short_usd": 200, "count": 3},
        ]
        refresh()
        expect(page.locator("#liq-combination")).to_have_text("尚未形成完整多空组合")
        expect(page.locator("#liq-state-rows tr.current")).to_have_count(0)
        assert page.locator("#liq-periods .liq-period-state").all_text_contents() == ["多空均衡", "暂无清算", "空头清算占优", "数据缺失"]
        assert "50.0%" in page.locator("#liq-periods .info-metric").nth(0).inner_text()
        assert "多头占比 — / 空头占比 —" in page.locator("#liq-periods .info-metric").nth(1).inner_text()
        state["custom"] = None
        state["pattern"] = "空空多多"
        refresh()
        expect(page.locator("#liq-analysis-description")).to_have_text("近1H、4H空头清算占优，12H、24H多头清算占优，近期窗口与较宽窗口结构不同。")

    page.locator("#liq-symbol-search").fill("BTCUSDT")
    page.locator('[data-symbol="BTCUSDT"]').click()
    page.wait_for_function("() => window.__testActiveRequests === 0")
    page.wait_for_load_state("networkidle")
    page.locator("#liq-side").select_option("short")
    page.locator("#liq-field").select_option("quantity")
    page.locator("#liq-min").fill("500")
    page.get_by_role("button", name="应用筛选", exact=True).click()
    page.wait_for_function("() => window.__testActiveRequests === 0")
    page.wait_for_load_state("networkidle")
    summary, detail = check_queries()
    assert {k: detail[k] for k in ["symbol", "side", "field", "minimum"]} == {"symbol": "BTCUSDT", "side": "short", "field": "quantity", "minimum": "500"}
    if not production:
        expect(page.locator("#liq-combination")).to_have_text("空空多多")
        expect(page.locator("#liq-rows")).to_contain_text("BTCUSDT")
        anchor = summary["to"]
        page.locator("#liq-next").click()
        page.wait_for_load_state("networkidle")
        expect(page.locator("#liq-page")).to_contain_text("第 2 页")
        assert check_queries()[0]["to"] == anchor
        expect(page.locator("#liq-analysis-status")).to_contain_text("历史分页保持此截止时间")
        page.locator("#liq-prev").click()
        page.wait_for_load_state("networkidle")
        state["failure"] = True
        state["rows_tag"] = "new-detail"
        page.locator("#liq-min").fill("0")
        page.get_by_role("button", name="应用筛选", exact=True).click()
        page.wait_for_load_state("networkidle")
        expect(page.locator("#liq-analysis-status")).to_contain_text("保留上次成功数据")
        expect(page.locator("#liq-page")).to_contain_text("第 1 页")
        expect(page.locator("#page-error")).to_be_hidden()
        expect(page.locator("#liq-combination")).to_have_text("空空多多")
        expect(page.locator("#liq-rows")).to_contain_text("321")
        state["failure"] = False
        state["detail_failure"] = True
        state["pattern"] = "多多多多"
        refresh()
        expect(page.locator("#liq-combination")).to_have_text("多多多多")
        expect(page.locator("#page-error")).to_contain_text("清算明细更新失败")
        state["detail_failure"] = False
        state["pattern"] = "空空多多"
        refresh()

    # Restore unfiltered details and explicitly exercise the actual 5-second callback.
    page.locator("#liq-side").select_option("all")
    page.locator("#liq-field").select_option("notional_usd")
    page.locator("#liq-min").fill("0")
    page.locator("#liq-symbol-all").click()
    page.wait_for_function("() => window.__testActiveRequests === 0")
    page.wait_for_load_state("networkidle")
    before = len(records)
    with page.expect_response(lambda response: "/api/v1/liquidations?" in response.url and "limit=1&" in response.url):
        page.evaluate("window.__testIntervals.find(timer => timer.ms === 5000).fn()")
    page.wait_for_function("() => window.__testActiveRequests === 0")
    page.wait_for_load_state("networkidle")
    assert len(records) >= before + 2, {"before": before, "after": len(records), "recent_queries": records[-6:]}
    summary, detail = check_queries()
    assert detail["symbol"] == "ALL"

    if production:
        query = "&".join(k + "=" + v for k, v in summary.items())
        actual = context.request.get(base + "/api/v1/liquidations?" + query).json()["data"]["periods"]
        by_label = {p["label"]: p for p in actual}
        expected_states = []
        pattern = ""
        for label in labels:
            p = by_label[label]
            state_name = "暂无清算" if p["long_usd"] + p["short_usd"] == 0 else "多空均衡" if p["long_usd"] == p["short_usd"] else "多头清算占优" if p["long_usd"] > p["short_usd"] else "空头清算占优"
            expected_states.append(state_name)
            pattern += "多" if state_name == "多头清算占优" else "空" if state_name == "空头清算占优" else "?"
        assert page.locator("#liq-periods .liq-period-state").all_text_contents() == expected_states
        expect(page.locator("#liq-combination")).to_have_text(pattern if "?" not in pattern else "尚未形成完整多空组合")

    for theme in ["dark", "light"]:
        page.evaluate("theme => window.LiquidationTheme.set(theme)", theme)
        for width in [1440, 390]:
            page.set_viewport_size({"width": width, "height": 1100 if width == 1440 else 844})
            assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 2"), (theme, width)
            page.screenshot(path=str(artifacts / ("eth-analysis-" + ("production-" if production else "fixture-") + theme + "-" + str(width) + ".png")), full_page=True)
            colors = page.locator("#liq-periods .info-metric").evaluate_all("cards => cards.map(card => getComputedStyle(card).backgroundColor)")
            if not production:
                expected = ["rgb(54, 26, 35)"] * 2 + ["rgb(12, 48, 33)"] * 2 if theme == "dark" else ["rgb(252, 231, 236)"] * 2 + ["rgb(228, 245, 233)"] * 2
                assert colors == expected, (theme, colors)
    assert not errors, errors
    if not production:
        state["failure"] = True
        first_failure = context.new_page()
        first_failure.route("**/api/v1/liquidations?**", fixture)
        first_failure.goto(base + "/liquidations", wait_until="networkidle")
        expect(first_failure.locator("#liq-analysis-status")).to_contain_text("等待有效数据")
        expect(first_failure.locator("#liq-periods .info-metric")).to_have_count(4)
        expect(first_failure.locator("#liq-combination")).to_have_text("尚未形成完整多空组合")
        expect(first_failure.locator("#liq-rows")).to_contain_text("321")
        first_failure.close()
    print(json.dumps({"production": production, "all_16_combinations": not production, "fixed_eth_scope": True, "independent_filters": True, "paired_cutoff": True, "five_second_refresh": True, "failure_and_recovery": not production, "themes": ["dark", "light"], "widths": [1440, 390], "javascript_errors": errors, "live_periods": actual if production else None}, ensure_ascii=False))
    context.close()
    browser.close()
