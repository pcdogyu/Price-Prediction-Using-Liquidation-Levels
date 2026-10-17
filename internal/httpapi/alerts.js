'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const base = document.querySelector('meta[name="app-base"]').content;
  const escape = value => String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
  const when = value => value ? new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }) : '—';
  const money = value => Number.isFinite(value) ? (value >= 1e6 ? '$' + (value / 1e6).toFixed(2) + 'M' : value >= 1e3 ? '$' + (value / 1e3).toFixed(0) + 'K' : '$' + value.toFixed(0)) : '—';
  const price = value => Number.isFinite(value) && value > 0 ? (value >= 1000 ? '$' + value.toFixed(2) : value >= 1 ? '$' + value.toFixed(4) : '$' + value.toFixed(6)) : '—';
  const span = seconds => {
    if (!Number.isFinite(seconds) || seconds < 0) return '—';
    const minutes = Math.floor(seconds / 60), rest = Math.round(seconds % 60);
    return minutes >= 60 ? Math.floor(minutes / 60) + ' 小时 ' + (minutes % 60) + ' 分' : minutes > 0 ? minutes + ' 分 ' + String(rest).padStart(2, '0') + ' 秒' : rest + ' 秒';
  };
  const sideLabel = side => side === 'long' ? '做多' : side === 'short' ? '做空' : '—';
  const liqLabel = side => side === 'long' ? '多单爆仓' : side === 'short' ? '空单爆仓' : '—';
  const cycleStates = { idle: '空闲', watching: '预警观察中', aggressive: '激进 · 等待反向确认', steady: '稳健 · 已确认', expired: '已过期' };
  const monitorStates = { idle: '监控中', watching: '预警观察中', aggressive: '激进 · 等待确认' };
  const notifyLabels = { queued: '已入队', suppressed: '已抑制', muted: '类别关闭', restored: '重启回放', disabled: '通知关闭' };
  const stateClass = state => state === 'watching' ? 'warning' : state === 'aggressive' || state === 'steady' ? 'buy' : 'muted';
  // Every pair name links to its own K-line page in a new tab, so the alert page
  // keeps its scroll position and filters while the chart opens beside it.
  const chartLink = symbol => '<a class="alerts-symbol-link" target="_blank" rel="noopener" href="' + base + 'alerts/chart?symbol=' + encodeURIComponent(symbol) + '" title="在新标签页打开 ' + escape(symbol) + ' K线">' + escape(symbol) + '</a>';

  const triggerLink = (recordType, recordID, symbol, side, referencePrice) => {
    if (!recordID || !Number.isFinite(referencePrice) || referencePrice <= 0) return '<span class="muted">不可触发</span>';
    return '<button type="button" class="alerts-trigger alerts-action alerts-order-trigger" data-action="order" data-trigger-type="' + escape(recordType) + '" data-trigger-id="' + escape(recordID) + '" data-trigger-symbol="' + escape(symbol) + '" data-trigger-side="' + escape(sideLabel(side)) + '">触发下单</button>';
  };

  const notifyLink = (recordType, recordID) => {
    if (!recordID) return '<span class="muted">不可触发</span>';
    return '<button type="button" class="alerts-action alerts-notify-trigger" data-action="notify" data-trigger-type="' + escape(recordType) + '" data-trigger-id="' + escape(recordID) + '">触发报价</button>';
  };

  let overviewSequence = 0, historySequence = 0, historyRows = [], historyBusy = false, historyPage = 0, historyCursors = [''], historyNextCursor = '', settingsLoaded = false, orderSignalSettingsLoaded = false, strategyLoaded = false;

  function error(message) { $('alerts-error').textContent = message; $('alerts-error').hidden = false; }
  function clearError() { $('alerts-error').hidden = true; }

  function metrics(data) {
    const cells = [
      ['监控币对', data.monitor.length + ' / ' + data.thresholds.top_symbols, ''],
      ['预警总数', data.alert_total, ''],
      ['激进信号', data.aggressive_total, 'buy'],
      ['稳健信号', data.steady_total, 'buy'],
      ['预警窗口', data.thresholds.window_minutes + ' 分钟', ''],
      ['安静期 / 确认窗口', data.thresholds.quiet_minutes + ' 分 / ' + data.thresholds.confirm_minutes + ' 分', '']
    ];
    $('alerts-metrics').innerHTML = cells.map(cell => '<div class="info-metric"><div class="label">' + escape(cell[0]) + '</div><div class="number ' + cell[2] + '">' + escape(String(cell[1])) + '</div></div>').join('');
  }

  function monitorRows(rows) {
    if (!rows.length) { $('alerts-monitor-rows').innerHTML = '<tr><td colspan="9">等待监控数据</td></tr>'; return; }
    $('alerts-monitor-rows').innerHTML = rows.map(row => '<tr' + (row.state === 'idle' ? '' : ' class="alerts-row-active"') + '>' +
      '<td>' + escape(row.rank) + '</td><td>' + chartLink(row.symbol) + '</td>' +
      '<td>' + money(row.quote_volume_usd) + '</td><td>' + money(row.threshold_usd) + '</td>' +
      '<td>' + escape(row.window_minutes || '—') + ' 分钟</td>' +
      '<td' + (row.long_usd >= row.threshold_usd ? ' class="buy"' : '') + '>' + money(row.long_usd) + (row.long_count ? '<span class="muted"> · ' + row.long_count + ' 笔</span>' : '') + '</td>' +
      '<td' + (row.short_usd >= row.threshold_usd ? ' class="sell"' : '') + '>' + money(row.short_usd) + (row.short_count ? '<span class="muted"> · ' + row.short_count + ' 笔</span>' : '') + '</td>' +
      '<td class="' + stateClass(row.state) + '">' + escape(monitorStates[row.state] || row.state) + (row.active_side ? '<span class="muted"> · ' + escape(liqLabel(row.active_side)) + '</span>' : '') + '</td>' +
      '<td>' + when(row.quiet_until) + '</td></tr>').join('');
  }

  function signalCells(signal) {
    const detail = signal.kind === 'steady'
      ? '反向爆仓 ' + money(signal.opposite_usd) + ' · ' + (signal.opposite_count || 0) + ' 笔 · ' + span(signal.opposite_elapsed_seconds)
      : '安静 ' + span(signal.quiet_elapsed_seconds) + ' · 顺延 ' + (signal.follow_ups || 0) + ' 次';
    return '<td>' + when(signal.created_at) + '</td><td>' + chartLink(signal.symbol) + '</td>' +
      '<td class="' + (signal.kind === 'steady' ? 'buy' : 'warning') + '">' + (signal.kind === 'steady' ? '稳健' : '激进') + '</td>' +
      '<td>' + escape(sideLabel(signal.side)) + '<span class="muted"> · ' + escape(liqLabel(signal.liquidation_side)) + '</span></td>' +
      '<td>' + money(signal.alert_notional_usd) + '<span class="muted"> / ' + money(signal.threshold_usd) + '</span></td>' +
      '<td>' + detail + '</td><td>' + price(signal.reference_price) + '</td><td>' + triggerLink(signal.kind, signal.id, signal.symbol, signal.side, signal.reference_price) + '</td><td>' + notifyLink(signal.kind, signal.id) + '</td>' +
      '<td>' + escape(notifyLabels[signal.notify_status] || signal.notify_status || '未推送') + (signal.notify_error ? '<span class="muted" title="' + escape(signal.notify_error) + '"> ⓘ</span>' : '') + '</td>';
  }

  function alertCells(row) {
    const detail = escape(row.event_count || 0) + ' 笔 · 顺延 ' + escape(row.follow_ups || 0) + ' 次 · ' +
      '<span class="' + stateClass(row.status) + '">' + escape(cycleStates[row.status] || row.status) + '</span>' +
      (row.quiet_until ? '<span class="muted">安静至 ' + when(row.quiet_until) + '</span>' : '');
    return '<td>' + when(row.triggered_at) + '</td><td>' + chartLink(row.symbol) + '</td>' +
      '<td class="warning">预警</td>' +
      '<td>' + escape(sideLabel(row.trade_side)) + '<span class="muted"> · ' + escape(liqLabel(row.side)) + '</span></td>' +
      '<td>' + money(row.notional_usd) + '<span class="muted"> / ' + money(row.threshold_usd) + '</span></td>' +
      '<td>' + detail + '</td><td>' + price(row.reference_price) + '</td><td>' + triggerLink('alert', row.id, row.symbol, row.trade_side, row.reference_price) + '</td><td>' + notifyLink('alert', row.id) + '</td><td>—</td>';
  }

  function timelineRows(rows) {
    const target = $('alerts-history-rows');
    if (!rows.length) { target.innerHTML = '<tr><td colspan="10">尚无历史记录</td></tr>'; return; }
    target.innerHTML = rows.map(row => '<tr>' + (row.type === 'alert' ? alertCells(row.alert) : signalCells(row.signal)) + '</tr>').join('');
  }

  function renderSettings(settings) {
    if (!settings) return;
    if (document.activeElement !== $('alerts-chat')) $('alerts-chat').value = settings.telegram_chat_id || '';
    if (!settingsLoaded) {
      $('alerts-enabled').checked = !!settings.telegram_enabled;
      $('alerts-notify-alerts').checked = !!settings.notify_alerts;
      $('alerts-notify-aggressive').checked = !!settings.notify_aggressive;
      $('alerts-notify-steady').checked = !!settings.notify_steady;
      settingsLoaded = true;
    }
    renderOrderSignalSettings(settings);
    const source = ({ database: '数据库', environment: '环境变量', unset: '未设置' })[settings.token_source] || settings.token_source;
    $('alerts-token-state').textContent = 'Token：' + (settings.has_token ? settings.token_mask : '未配置') + ' · 来源 ' + source + ' · 保持留空即沿用现有 Token' + (settings.updated_at ? ' · 更新 ' + when(settings.updated_at) : '');
  }

  function renderOrderSignalSummary() {
    const selected = [
      [$('alerts-order-aggressive').checked, '激进开仓'],
      [$('alerts-order-steady').checked, '稳健开仓']
    ].filter(item => item[0]).map(item => item[1]);
    $('alerts-order-signal-summary').textContent = '预平仓始终开启' + (selected.length ? ' · 已开启：' + selected.join('、') : ' · 当前不自动开仓');
  }

  function renderOrderSignalSettings(settings) {
    if (!settings) return;
    if (!orderSignalSettingsLoaded) {
      $('alerts-order-aggressive').checked = !!settings.order_aggressive;
      $('alerts-order-steady').checked = !!settings.order_steady;
      orderSignalSettingsLoaded = true;
    }
    renderOrderSignalSummary();
  }

  function renderNotifyStatus(notifications) {
    if (!notifications) return;
    const parts = [notifications.enabled ? '推送已启用' : '推送已关闭', notifications.configured ? '凭据就绪' : '缺少 Token 或 Chat ID'];
    parts.push('队列 ' + (notifications.queue_depth || 0), '已发送 ' + (notifications.sent || 0), '已抑制 ' + (notifications.suppressed || 0));
    if (notifications.failed) parts.push('失败 ' + notifications.failed);
    if (notifications.last_error) parts.push('最近错误：' + notifications.last_error);
    if (notifications.last_success_at) parts.push('最近成功 ' + when(notifications.last_success_at));
    $('alerts-notify-status').textContent = parts.join(' · ');
  }

  function renderStrategySettings(settings) {
    if (!settings) return;
    const fields = [['alerts-threshold-btc', settings.btc_usd], ['alerts-threshold-eth', settings.eth_usd], ['alerts-threshold-other', settings.other_usd], ['alerts-window-minutes', settings.window_minutes]];
    for (const [id, value] of fields) {
      const input = $(id);
      if (document.activeElement !== input && Number.isFinite(Number(value))) input.value = value;
    }
    $('alerts-monitor-window').textContent = settings.window_minutes || 3;
    $('alerts-subtitle').textContent = '成交额前 ' + (settings.top_symbols || 100) + ' 币对 · ' + (settings.window_minutes || 3) + ' 分钟爆仓预警 · 激进 / 稳健下单信号';
    if (!strategyLoaded) strategyLoaded = true;
  }

  async function loadOverview() {
    overviewSequence++;
    const current = overviewSequence;
    try {
      const response = await fetch(base + 'api/v1/alerts', { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 401) { location.assign(base); return; }
      const packet = await response.json();
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      if (current !== overviewSequence) return;
      const data = packet.data;
      clearError();
      metrics(data);
      monitorRows(data.monitor);
      renderStrategySettings(data.thresholds);
      $('alerts-method').textContent = data.method;
      const states = data.monitor.filter(row => row.state !== 'idle').length;
      $('alerts-monitor-status').textContent = ({ waiting_watch_list: '等待成交额排行', stale: '成交额排名已过期' })[data.state] || ('已加载 ' + data.monitor.length + ' 个币对 · ' + states + ' 个处于预警流程 · 每 5 秒更新');
      $('alerts-monitor-note').textContent = '排名更新 ' + when(data.watch_updated_at) + (data.watch_error ? ' · 最近刷新失败：' + data.watch_error : '') + ' · 预警与信号自 ' + when(data.available_from) + ' 起可查询';
      renderSettings(packet.settings);
      renderNotifyStatus(data.notifications);
    } catch (caught) {
      if (current !== overviewSequence) return;
      error('报警更新失败：' + caught.message);
      $('alerts-monitor-status').textContent = '更新失败 · 保留上次数据';
    }
  }


  async function loadHistory(cursor, page) {
    historySequence++;
    const current = historySequence;
    historyBusy = true;
    const params = new URLSearchParams({ symbol: $('alerts-symbol').value.trim(), kind: $('alerts-kind').value });
    if (cursor) params.set('cursor', cursor);
    $('alerts-history-status').textContent = '正在读取…';
    $('alerts-prev').disabled = true;
    $('alerts-next').disabled = true;
    try {
      const response = await fetch(base + 'api/v1/alerts/timeline?' + params, { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 401) { location.assign(base); return; }
      const packet = await response.json();
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      if (current !== historySequence) return;
      historyRows = packet.rows || [];
      historyPage = page;
      historyNextCursor = packet.next_cursor || '';
      timelineRows(historyRows);
      $('alerts-history-status').textContent = '预警 ' + packet.alert_count + ' · 激进 ' + packet.aggressive_count + ' · 稳健 ' + packet.steady_count + ' · 共 ' + packet.total_count + ' 条';
      $('alerts-count').textContent = '第 ' + (historyPage + 1) + ' 页 · 本页 ' + historyRows.length + ' 条';
      $('alerts-prev').disabled = historyPage === 0;
      $('alerts-next').disabled = !historyNextCursor;
      clearError();
    } catch (caught) {
      if (current !== historySequence) return;
      error('预警与信号历史读取失败：' + caught.message);
      $('alerts-history-status').textContent = '读取失败';
    } finally {
      if (current === historySequence) historyBusy = false;
    }
  }

  function settingsBody() {
    const body = {
      telegram_enabled: $('alerts-enabled').checked,
      notify_alerts: $('alerts-notify-alerts').checked,
      notify_aggressive: $('alerts-notify-aggressive').checked,
      notify_steady: $('alerts-notify-steady').checked,
      order_alerts: true,
      order_aggressive: $('alerts-order-aggressive').checked,
      order_steady: $('alerts-order-steady').checked,
      telegram_chat_id: $('alerts-chat').value.trim()
    };
    const token = $('alerts-token').value.trim();
    if (token) body.telegram_token = token;
    return body;
  }

  function strategySettingsBody() {
    return {
      btc_usd: Number($('alerts-threshold-btc').value),
      eth_usd: Number($('alerts-threshold-eth').value),
      other_usd: Number($('alerts-threshold-other').value),
      window_minutes: Number($('alerts-window-minutes').value)
    };
  }

  function orderSignalSettingsBody() {
    return {
      order_alerts: true,
      order_aggressive: $('alerts-order-aggressive').checked,
      order_steady: $('alerts-order-steady').checked
    };
  }

  async function saveStrategySettings(event) {
    event.preventDefault();
    $('alerts-strategy-save').disabled = true;
    $('alerts-strategy-status').textContent = '正在保存报警参数…';
    try {
      const response = await fetch(base + 'api/v1/alerts/strategy-settings', { method: 'POST', cache: 'no-store', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(strategySettingsBody()) });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      renderStrategySettings(packet);
      $('alerts-strategy-status').textContent = '报警参数已保存并立即生效';
      clearError();
      loadOverview();
    } catch (caught) {
      error('保存报警参数失败：' + caught.message);
      $('alerts-strategy-status').textContent = '保存失败';
    } finally {
      $('alerts-strategy-save').disabled = false;
    }
  }

  async function saveOrderSignalSettings(event) {
    event.preventDefault();
    $('alerts-order-signal-save').disabled = true;
    $('alerts-order-signal-status').textContent = '正在保存下单信号设置…';
    try {
      const response = await fetch(base + 'api/v1/alerts/order-settings', { method: 'POST', cache: 'no-store', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(orderSignalSettingsBody()) });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      renderOrderSignalSettings(packet);
      $('alerts-order-signal-status').textContent = '下单信号设置已保存并立即生效';
      clearError();
    } catch (caught) {
      error('保存下单信号设置失败：' + caught.message);
      $('alerts-order-signal-status').textContent = '保存失败';
    } finally {
      $('alerts-order-signal-save').disabled = false;
    }
  }

  async function saveSettings(event) {
    event.preventDefault();
    $('alerts-save').disabled = true;
    $('alerts-notify-status').textContent = '正在保存设置…';
    try {
      const response = await fetch(base + 'api/v1/alerts/settings', { method: 'POST', cache: 'no-store', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(settingsBody()) });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      $('alerts-token').value = '';
      renderSettings(packet);
      $('alerts-notify-status').textContent = '设置已保存';
      clearError();
      loadOverview();
    } catch (caught) {
      error('保存通知设置失败：' + caught.message);
      $('alerts-notify-status').textContent = '保存失败';
    } finally {
      $('alerts-save').disabled = false;
    }
  }

  async function testSettings() {
    $('alerts-test').disabled = true;
    $('alerts-notify-status').textContent = '正在发送测试消息…';
    try {
      const response = await fetch(base + 'api/v1/alerts/settings/test', { method: 'POST', cache: 'no-store', credentials: 'same-origin' });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      $('alerts-notify-status').textContent = '测试消息已送达，请在 Telegram 查看';
      clearError();
    } catch (caught) {
      error('Telegram 测试失败：' + caught.message);
      $('alerts-notify-status').textContent = '测试失败';
    } finally {
      $('alerts-test').disabled = false;
    }
  }

  async function triggerOrder(button) {
    button.disabled = true;
    const original = button.textContent;
    button.textContent = '下单中';
    try {
      const response = await fetch(base + 'api/v1/alerts/trigger', { method: 'POST', cache: 'no-store', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ record_type: button.dataset.triggerType, record_id: button.dataset.triggerId }) });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      button.textContent = original;
      button.disabled = false;
      clearError();
    } catch (caught) {
      button.disabled = false;
      button.textContent = original;
      error('手动下单信号发送失败：' + caught.message);
    }
  }

  async function triggerNotify(button) {
    button.disabled = true;
    const original = button.textContent;
    button.textContent = '发送中';
    try {
      const response = await fetch(base + 'api/v1/alerts/notify', { method: 'POST', cache: 'no-store', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ record_type: button.dataset.triggerType, record_id: button.dataset.triggerId }) });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      button.textContent = '已发送';
      clearError();
      setTimeout(() => { button.textContent = original; button.disabled = false; }, 2200);
    } catch (caught) {
      button.disabled = false;
      button.textContent = original;
      error('手动报警发送失败：' + caught.message);
    }
  }

  $('alerts-filters').addEventListener('submit', event => { event.preventDefault(); historyCursors = ['']; loadHistory('', 0); });
  $('alerts-prev').addEventListener('click', () => { if (historyPage > 0) loadHistory(historyCursors[historyPage - 1], historyPage - 1); });
  $('alerts-next').addEventListener('click', () => { if (!historyNextCursor) return; historyCursors[historyPage + 1] = historyNextCursor; loadHistory(historyNextCursor, historyPage + 1); });
  $('alerts-settings').addEventListener('submit', saveSettings);
  $('alerts-strategy-settings').addEventListener('submit', saveStrategySettings);
  $('alerts-order-signal-settings').addEventListener('submit', saveOrderSignalSettings);
  ['alerts-order-aggressive', 'alerts-order-steady'].forEach(id => $(id).addEventListener('change', renderOrderSignalSummary));
  $('alerts-test').addEventListener('click', testSettings);
  $('alerts-history-rows').addEventListener('click', event => {
    const button = event.target.closest('.alerts-action');
    if (!button) return;
    if (button.dataset.action === 'notify') triggerNotify(button);
    else triggerOrder(button);
  });
  loadOverview();
  loadHistory('', 0);
  setInterval(() => { if (!document.hidden) { loadOverview(); if (historyPage === 0 && !historyBusy) loadHistory('', 0); } }, 5000);
})();

