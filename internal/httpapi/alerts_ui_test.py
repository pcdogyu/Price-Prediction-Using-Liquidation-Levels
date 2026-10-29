"""Real-browser regression for the unified alert and signal history."""
import datetime
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
root = pathlib.Path(__file__).resolve().parents[2]
artifacts = root / "build"
artifacts.mkdir(exist_ok=True)
errors, requests = [], []


def timeline_fixture():
    rows = []
    now = datetime.datetime.now(datetime.timezone.utc)
    for index in range(25):
        at = (now - datetime.timedelta(minutes=index)).isoformat().replace("+00:00", "Z")
        kind = ("alert", "aggressive", "steady")[index % 3]
        item = {"type": kind, "event_at": at}
        if kind == "alert":
            item["alert"] = {
                "id": f"a{index}", "symbol": "BTCUSDT", "side": "long", "trade_side": "long",
                "triggered_at": at, "notional_usd": 420000, "threshold_usd": 300000,
                "event_count": 4, "follow_ups": 1, "status": "watching", "quiet_until": at,
                "reference_price": 68000, "gamma_ignored": index == 0,
            }
        else:
            item["signal"] = {
                "id": f"s{index}", "symbol": "BTCUSDT", "kind": kind, "side": "long",
                "liquidation_side": "long", "created_at": at, "alert_notional_usd": 420000,
                "threshold_usd": 300000, "quiet_elapsed_seconds": 600, "follow_ups": 1,
                "opposite_usd": 120000, "opposite_count": 2, "opposite_elapsed_seconds": 90,
                "reference_price": 68000, "notify_status": "queued", "gamma_ignored": index == 0,
            }
        if index < 3:
            target = item["alert"] if kind == "alert" else item["signal"]
            target.update({"filter_applied": True, "open_allowed": False, "close_allowed": True})
        elif index == 3:
            target = item["alert"] if kind == "alert" else item["signal"]
            target.update({"filter_applied": True, "open_allowed": True, "close_allowed": True})
        elif index == 4:
            target = item["alert"] if kind == "alert" else item["signal"]
            target.update({
                "filter_applied": True,
                "open_allowed": False,
                "close_allowed": False,
                # Legacy rows do not contain filter_tags. This reason must not
                # be reverse-engineered into a false VWAP label.
                "filter_reason": "neutral_zscore_long_liquidation_close_only",
            })
        if index == 0:
            target = item["alert"] if kind == "alert" else item["signal"]
            target["filter_tags"] = ["vwap", "donchian"]
        elif index == 1:
            target = item["alert"] if kind == "alert" else item["signal"]
            target["filter_tags"] = ["vwap"]
        elif index == 2:
            target = item["alert"] if kind == "alert" else item["signal"]
            target["filter_tags"] = ["donchian"]
        rows.append(item)
    rows.insert(5, {
        "type": "value_area",
        "event_at": (now - datetime.timedelta(minutes=30)).isoformat().replace("+00:00", "Z"),
        "signal": {
            "id": "va-gamma-negative", "symbol": "ETHUSDT", "kind": "value_area", "side": "short",
            "liquidation_side": "short", "created_at": (now - datetime.timedelta(minutes=30)).isoformat().replace("+00:00", "Z"),
            "alert_notional_usd": 0, "threshold_usd": 0, "reference_price": 3200,
            "notify_status": "muted", "gamma_ignored": True, "gamma_value": -0.12,
            "gamma_state": "ok", "filter_applied": False, "open_allowed": False,
            "close_allowed": True, "position_effect": "close", "boundary": "vah",
            "boundary_price": 3200, "val": 3150, "vah": 3200,
        },
    })
    return rows


all_rows = timeline_fixture()
holding_options = [15, 60, 240, 480, 720, 1440]
backtest_rows = []
for signal in ("alert", "aggressive", "steady"):
    for filter_name in ("none", "vwap", "donchian", "vwap_donchian"):
        for holding_minutes in holding_options:
            backtest_rows.append({
                "signal": signal, "filter": filter_name, "holding_minutes": holding_minutes,
                "trades": 1, "additions": 0, "win_rate": 100, "profit_factor": 0,
                "max_drawdown_pct": 0, "final_equity": 10000,
            })
for holding_minutes in holding_options:
    backtest_rows.append({
        "signal": "value_area", "filter": "none", "holding_minutes": holding_minutes,
        "trades": 1, "additions": 0, "win_rate": 100, "profit_factor": 0,
        "max_drawdown_pct": 0, "final_equity": 10000,
    })
backtest_report = {
    "window_days": 14, "holding_minutes": 15, "holding_minutes_options": holding_options,
    "initial_equity": 10000, "leverage": 1, "take_profit_pct": 1.2, "stop_loss_pct": 1.5,
    "entry_notional_usd": 1000, "max_add_ons": 1, "vwap_range_hours": 24,
    "vwap_min_drawdown_pct": 2, "vwap_donchian_pct": 35, "symbol_count": 2,
    "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "rows": backtest_rows,
}

with sync_playwright() as playwright:
    launch = {"headless": True}
    chrome = pathlib.Path(r"C:\Program Files\Google\Chrome\Application\chrome.exe")
    if chrome.exists():
        launch["executable_path"] = str(chrome)
    browser = playwright.chromium.launch(**launch)
    context = browser.new_context(viewport={"width": 1440, "height": 1100})
    context.add_init_script("window.setInterval = () => 1;")
    page = context.new_page()
    page.on("pageerror", lambda error: errors.append(str(error)))

    def route_timeline(route):
        query = parse_qs(urlsplit(route.request.url).query)
        kind = query.get("kind", ["all"])[0]
        cursor = query.get("cursor", [""])[0]
        requests.append({"kind": kind, "cursor": cursor})
        selected = all_rows if kind == "all" else [row for row in all_rows if row["type"] == kind]
        offset = 20 if cursor else 0
        page_rows = selected[offset:offset + 20]
        packet = {
            "rows": page_rows,
            "total_count": len(selected),
            "alert_count": 9,
            "aggressive_count": 8,
            "steady_count": 8,
            "gamma": {"anchor_symbol": "ETHUSDT", "state": "ok", "gamma": -0.12345, "ignored": True},
        }
        if offset + 20 < len(selected):
            packet["next_cursor"] = "page-2"
        route.fulfill(status=200, json=packet)

    def route_backtest(route):
        if route.request.method == "POST":
            route.fulfill(status=200, json={"status": "completed", "report": backtest_report})
        else:
            route.fulfill(status=404, json={"detail": "not found"})

    page.route("**/api/v1/alerts/timeline?**", route_timeline)
    page.route("**/api/v1/alerts/backtest", route_backtest)
    page.goto(base + "/alerts", wait_until="domcontentloaded")
    expect(page.locator("#alerts-history-status")).to_contain_text("预警 9", timeout=30000)
    expect(page.locator("#alerts-history-rows tr")).to_have_count(20)
    expect(page.locator("#alerts-count")).to_have_text("第 1 页 · 本页 20 条")
    expect(page.locator("#alerts-prev")).to_be_disabled()
    expect(page.locator("#alerts-next")).to_be_enabled()
    expect(page.locator("#alerts-history-rows")).to_contain_text("预警")
    expect(page.locator("#alerts-history-rows")).to_contain_text("激进")
    expect(page.locator("#alerts-history-rows")).to_contain_text("稳健")
    expect(page.locator("#alerts-history-rows")).to_contain_text("VWAP过滤")
    expect(page.locator("#alerts-history-rows")).to_contain_text("Donchian过滤")
    expect(page.locator("#alerts-history-rows")).to_contain_text("交易所刷单策略过滤")
    expect(page.locator("#alerts-history-rows").locator("tr").nth(0).locator("td").nth(2)).to_contain_text("VWAP过滤 · Donchian过滤")
    expect(page.locator("#alerts-history-rows").locator("tr").nth(4).locator("td").nth(2)).not_to_contain_text("VWAP过滤")
    expect(page.locator("#alerts-history-rows").locator("tr").nth(4).locator("td").nth(2)).not_to_contain_text("Donchian过滤")
    expect(page.locator(".alerts-close-only-prefix")).to_have_count(4)
    expect(page.locator(".alerts-order-trigger")).to_have_count(20)
    expect(page.get_by_text("策略开关", exact=True)).to_be_visible()
    expect(page.locator("#alerts-history-rows")).to_contain_text("做多 gamma忽略")
    assert page.locator(".gamma-ignored").count() == 1
    expect(page.get_by_text("ETHUSDT Gamma：-0.123450", exact=False)).to_be_visible()

    page.locator("#alerts-next").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(6)
    expect(page.locator("#alerts-count")).to_have_text("第 2 页 · 本页 6 条")
    expect(page.locator("#alerts-prev")).to_be_enabled()
    expect(page.locator("#alerts-next")).to_be_disabled()
    page.locator("#alerts-prev").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(20)

    page.locator("#alerts-kind").select_option("alert")
    page.locator("#alerts-apply").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(9)
    expect(page.locator("#alerts-count")).to_have_text("第 1 页 · 本页 9 条")
    expect(page.locator("#alerts-history-rows td:nth-child(3)")).to_contain_text(["预警"] * 9)

    page.locator("#alerts-backtest-run").click()
    expect(page.locator("#alerts-backtest-rows tr")).to_have_count(78)
    expect(page.locator(".alerts-backtest-table th[data-sort-key='holding_minutes']")).to_contain_text("持有时间")
    expect(page.locator("#alerts-backtest-status")).to_contain_text("持有 15m、1h、4h、8h、12h、24h")
    expect(page.locator("#alerts-backtest-rows tr").nth(0).locator("td").nth(2)).to_have_text("15m")
    page.locator(".alerts-backtest-table th[data-sort-key='holding_minutes'] button").click()
    page.locator(".alerts-backtest-table th[data-sort-key='holding_minutes'] button").click()
    expect(page.locator("#alerts-backtest-rows tr").nth(0).locator("td").nth(2)).to_have_text("24h")

    for width in (1440, 390):
        page.set_viewport_size({"width": width, "height": 1100})
        for theme in ("dark", "light"):
            page.evaluate("theme => window.LiquidationTheme.set(theme)", theme)
            assert page.evaluate("() => document.documentElement.scrollWidth <= window.innerWidth")
            expect(page.get_by_text("每页 20 条", exact=True)).to_be_visible()
            page.screenshot(path=str(artifacts / f"alerts-unified-{width}-{theme}.png"), full_page=True)

    assert not errors, errors
    print(json.dumps({"status": "passed", "screenshots": 4, "requests": requests}, ensure_ascii=False))
    browser.close()
