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
        row_data = item["alert"] if kind == "alert" else item["signal"]
        if index == 8:
            row_data.pop("notify_status", None)
        else:
            row_data["notify_status"] = "muted" if index in (6, 7) else "queued"
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
        elif index == 5:
            target = item["alert"] if kind == "alert" else item["signal"]
            target.update({
                "filter_applied": True,
                "open_allowed": False,
                "close_allowed": True,
                "filter_reason": "price_not_below_val_long_liquidation_blocked",
                "filter_tags": ["value_area"],
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
    for filter_name in ("none", "vwap", "donchian", "vwap_donchian", "value_area", "vwap_value_area", "donchian_value_area", "vwap_donchian_value_area"):
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
    "value_area_covered_events": 18, "value_area_total_events": 24,
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
        notification = query.get("notification", [""])[0]
        cursor = query.get("cursor", [""])[0]
        requests.append({"kind": kind, "cursor": cursor, "notification": notification})
        selected = all_rows if kind == "all" else [row for row in all_rows if row["type"] == kind]
        if notification:
            def notification_status(row):
                data = row.get("alert") or row.get("signal") or {}
                return data.get("notify_status", "")
            if notification == "notified":
                selected = [row for row in selected if notification_status(row) in ("queued", "sent")]
            elif notification == "not_notified":
                selected = [row for row in selected if notification_status(row) and notification_status(row) not in ("queued", "sent")]
            elif notification == "unknown":
                selected = [row for row in selected if not notification_status(row)]
        offset = 20 if cursor else 0
        page_rows = selected[offset:offset + 20]
        packet = {
            "rows": page_rows,
            "total_count": len(selected),
            "alert_count": sum(row["type"] == "alert" for row in selected),
            "aggressive_count": sum(row["type"] == "aggressive" for row in selected),
            "steady_count": sum(row["type"] == "steady" for row in selected),
            "value_area_count": sum(row["type"] == "value_area" for row in selected),
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

    strategy_posts = []
    strategy_state = {"value_area_filter_enabled": True}
    def route_strategy_settings(route):
        if route.request.method == "POST":
            body = json.loads(route.request.post_data or "{}")
            strategy_posts.append(body)
            strategy_state.update(body)
            route.fulfill(status=200, json=strategy_state)
        else:
            route.continue_()


    def route_overview(route):
        response = route.fetch()
        packet = response.json()
        packet.setdefault("data", {}).setdefault("thresholds", {})["value_area_filter_enabled"] = strategy_state["value_area_filter_enabled"]
        packet.setdefault("data", {})["gamma"] = {
            "anchor_symbol": "ETHUSDT", "state": "ok", "gamma": -0.12345, "ignored": True,
        }
        route.fulfill(response=response, json=packet)

    page.route("**/api/v1/alerts/timeline?**", route_timeline)
    page.route("**/api/v1/alerts/backtest", route_backtest)
    page.route("**/api/v1/alerts/strategy-settings", route_strategy_settings)
    page.route("**/api/v1/alerts", route_overview)
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
    expect(page.locator("#alerts-history-rows")).to_contain_text("VAL/VAH过滤")
    expect(page.locator("#alerts-history-rows")).to_contain_text("交易所刷单策略过滤")
    expect(page.locator("#alerts-history-rows").locator("tr").nth(0).locator("td").nth(2)).to_contain_text("VWAP过滤 · Donchian过滤")
    expect(page.locator("#alerts-history-rows").locator("tr").nth(4).locator("td").nth(2)).not_to_contain_text("VWAP过滤")
    expect(page.locator("#alerts-history-rows").locator("tr").nth(4).locator("td").nth(2)).not_to_contain_text("Donchian过滤")
    expect(page.locator(".alerts-close-only-prefix")).to_have_count(5)
    expect(page.locator(".alerts-order-trigger")).to_have_count(20)
    expect(page.get_by_text("策略开关", exact=True)).to_be_visible()
    expect(page.locator("#alerts-history-rows")).to_contain_text("做多 gamma忽略")
    assert page.locator(".gamma-ignored").count() == 2
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
    expect(page.locator("#alerts-filter option[value='unfiltered']")).to_have_text("未运行过滤器")
    page.reload(wait_until="domcontentloaded")
    expect(page.locator("#alerts-kind")).to_have_value("alert")
    expect(page.locator("#alerts-history-rows tr")).to_have_count(9)

    page.locator("#alerts-kind").select_option("all")
    page.locator("#alerts-notification-filter").select_option("not_notified")
    page.locator("#alerts-apply").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(3)
    expect(page.locator("#alerts-history-rows")).to_contain_text("类别关闭")
    assert requests[-1]["notification"] == "not_notified"
    page.locator("#alerts-notification-filter").select_option("unknown")
    page.locator("#alerts-apply").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(1)
    expect(page.locator("#alerts-history-rows")).to_contain_text("未记录")
    assert requests[-1]["notification"] == "unknown"
    page.locator("#alerts-notification-filter").select_option("")
    page.locator("#alerts-apply").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(20)

    value_area_filter = page.locator("#alerts-value-area-filter")
    expect(value_area_filter).to_be_checked()
    value_area_filter.uncheck()
    expect(page.locator("#alerts-strategy-status")).to_contain_text("已自动保存", timeout=10000)
    assert strategy_posts and strategy_posts[-1]["value_area_filter_enabled"] is False
    expect(value_area_filter).not_to_be_checked()
    value_area_filter.check()
    expect(page.locator("#alerts-strategy-status")).to_contain_text("已自动保存", timeout=10000)
    assert strategy_posts[-1]["value_area_filter_enabled"] is True

    page.locator("#alerts-backtest-run").click()
    expect(page.locator("#alerts-backtest-rows tr")).to_have_count(150)
    expect(page.locator("#alerts-backtest-value-area-coverage")).to_have_text("VAL/VAH 快照覆盖 18 / 24 个爆仓事件")
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
            assert page.evaluate("() => document.documentElement.scrollWidth <= window.innerWidth"), f"horizontal overflow at {width}px in {theme} theme"
            expect(page.get_by_text("每页 20 条", exact=True)).to_be_visible()
            page.screenshot(path=str(artifacts / f"alerts-unified-{width}-{theme}.png"), full_page=True)

    assert not errors, errors
    print(json.dumps({"status": "passed", "screenshots": 4, "requests": requests}, ensure_ascii=False))
    browser.close()
