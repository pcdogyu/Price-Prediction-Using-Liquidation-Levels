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
        rows.append(item)
    return rows


all_rows = timeline_fixture()

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
            "gamma": {"anchor_symbol": "ETHUSDT", "state": "ok", "net_gex_usd": -123.45, "ignored": True},
        }
        if offset + 20 < len(selected):
            packet["next_cursor"] = "page-2"
        route.fulfill(status=200, json=packet)

    page.route("**/api/v1/alerts/timeline?**", route_timeline)
    page.goto(base + "/alerts", wait_until="domcontentloaded")
    expect(page.locator("#alerts-history-status")).to_contain_text("预警 9", timeout=30000)
    expect(page.locator("#alerts-history-rows tr")).to_have_count(20)
    expect(page.locator("#alerts-count")).to_have_text("第 1 页 · 本页 20 条")
    expect(page.locator("#alerts-prev")).to_be_disabled()
    expect(page.locator("#alerts-next")).to_be_enabled()
    expect(page.locator("#alerts-history-rows")).to_contain_text("预警")
    expect(page.locator("#alerts-history-rows")).to_contain_text("激进")
    expect(page.locator("#alerts-history-rows")).to_contain_text("稳健")
    expect(page.get_by_text("下单信号类别", exact=True)).to_be_visible()
    expect(page.locator("#alerts-history-rows")).to_contain_text("做多 gamma忽略")
    assert page.locator(".gamma-ignored").count() == 1
    expect(page.get_by_text("ETHUSDT Gamma：NetGEX $-123", exact=False)).to_be_visible()

    page.locator("#alerts-next").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(5)
    expect(page.locator("#alerts-count")).to_have_text("第 2 页 · 本页 5 条")
    expect(page.locator("#alerts-prev")).to_be_enabled()
    expect(page.locator("#alerts-next")).to_be_disabled()
    page.locator("#alerts-prev").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(20)

    page.locator("#alerts-kind").select_option("alert")
    page.locator("#alerts-apply").click()
    expect(page.locator("#alerts-history-rows tr")).to_have_count(9)
    expect(page.locator("#alerts-count")).to_have_text("第 1 页 · 本页 9 条")
    expect(page.locator("#alerts-history-rows td:nth-child(3)")).to_have_text(["预警"] * 9)

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
