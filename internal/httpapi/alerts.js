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

  let overviewSequence = 0, historySequence = 0, historyCursor = '', historyRows = [], historyBusy = false, settingsLoaded = false;

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
    if (!rows.length) { $('alerts-monitor-rows').innerHTML = '<tr><td colspan="8">等待监控数据</td></tr>'; return; }
    $('alerts-monitor-rows').innerHTML = rows.map(row => '<tr' + (row.state === 'idle' ? '' : ' class="alerts-row-active"') + '>' +
      '<td>' + escape(row.rank) + '</td><td>' + chartLink(row.symbol) + '</td>' +
      '<td>' + money(row.quote_volume_usd) + '</td><td>' + money(row.threshold_usd) + '</td>' +
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
      '<td>' + detail + '</td><td>' + price(signal.reference_price) + '</td>' +
      '<td>' + escape(notifyLabels[signal.notify_status] || signal.notify_status || '未推送') + (signal.notify_error ? '<span class="muted" title="' + escape(signal.notify_error) + '"> ⓘ</span>' : '') + '</td>';
  }

  function signalRows(rows, empty) {
    const target = $('alerts-history-rows');
    if (!rows.length) { target.innerHTML = '<tr><td colspan="8">' + empty + '</td></tr>'; return; }
    target.innerHTML = rows.map(signal => '<tr>' + signalCells(signal) + '</tr>').join('');
  }

  function alertRows(rows) {
    if (!rows.length) { $('alerts-alert-rows').innerHTML = '<tr><td colspan="10">尚无预警记录</td></tr>'; return; }
    $('alerts-alert-rows').innerHTML = rows.map(row => '<tr>' +
      '<td>' + when(row.triggered_at) + '</td><td>' + chartLink(row.symbol) + '</td>' +
      '<td class="' + (row.side === 'long' ? 'buy' : 'sell') + '">' + escape(liqLabel(row.side)) + '</td>' +
      '<td>' + escape(sideLabel(row.trade_side)) + '</td>' +
      '<td>' + money(row.notional_usd) + '<span class="muted"> · 峰值 ' + money(row.peak_usd) + '</span></td>' +
      '<td>' + money(row.threshold_usd) + '</td><td>' + escape(row.event_count) + '</td><td>' + escape(row.follow_ups || 0) + '</td>' +
      '<td class="' + stateClass(row.status) + '">' + escape(cycleStates[row.status] || row.status) + '</td>' +
      '<td>' + when(row.quiet_until) + '</td></tr>').join('');
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
    const source = ({ database: '数据库', environment: '环境变量', unset: '未设置' })[settings.token_source] || settings.token_source;
    $('alerts-token-state').textContent = 'Token：' + (settings.has_token ? settings.token_mask : '未配置') + ' · 来源 ' + source + ' · 保持留空即沿用现有 Token' + (settings.updated_at ? ' · 更新 ' + when(settings.updated_at) : '');
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
      alertRows(data.alerts);
      syncLatestSignals(data.signals);
      $('alerts-method').textContent = data.method;
      const states = data.monitor.filter(row => row.state !== 'idle').length;
      $('alerts-monitor-status').textContent = ({ waiting_watch_list: '等待成交额排行', stale: '成交额排名已过期' })[data.state] || ('已加载 ' + data.monitor.length + ' 个币对 · ' + states + ' 个处于预警流程 · 每 5 秒更新');
      $('alerts-monitor-note').textContent = '排名更新 ' + when(data.watch_updated_at) + (data.watch_error ? ' · 最近刷新失败：' + data.watch_error : '') + ' · 预警与信号自 ' + when(data.available_from) + ' 起可查询';
      $('alerts-alert-status').textContent = '共 ' + data.alert_total + ' 条预警 · 最新 ' + data.alerts.length + ' 条已显示';
      $('alerts-alert-note').textContent = '“顺延”记录预警后同方向继续爆仓的次数，每次顺延都会重新计算安静截止。状态为过期的预警表示确认窗口内未出现反向爆仓，只保留激进信号。';
      renderSettings(packet.settings);
      renderNotifyStatus(data.notifications);
    } catch (caught) {
      if (current !== overviewSequence) return;
      error('报警更新失败：' + caught.message);
      $('alerts-monitor-status').textContent = '更新失败 · 保留上次数据';
    }
  }


  // historyFiltersAreAll reports whether the signal table shows every stored signal,
  // the only state in which a poll may fold new rows in without contradicting a filter.
  function historyFiltersAreAll() {
    return !$('alerts-symbol').value.trim() && $('alerts-kind').value === 'all';
  }

  // syncLatestSignals keeps the one signal table live. The overview poll carries the
  // newest stored signals, so a row that is missing locally and newer than the top
  // loaded row has just arrived: the visible page reloads, widened to cover whatever the
  // reader already paged through, so rows and counts stay true. Past the page ceiling the
  // fresh rows are folded in by hand instead of disturbing the loaded pages.
  function syncLatestSignals(signals) {
    if (!Array.isArray(signals) || !signals.length || historyBusy || !historyFiltersAreAll()) return;
    const loaded = new Set(historyRows.map(row => row.id));
    const newest = historyRows.length ? Date.parse(historyRows[0].created_at) : -1;
    const fresh = signals.filter(row => !loaded.has(row.id) && (newest < 0 || Date.parse(row.created_at) > newest));
    if (!fresh.length) return;
    if (historyRows.length + fresh.length <= 200) {
      loadHistory(false, Math.max(Number($('alerts-limit').value), historyRows.length + fresh.length));
      return;
    }
    historyRows = fresh.concat(historyRows);
    signalRows(historyRows, '尚无信号');
    $('alerts-count').textContent = '已显示 ' + historyRows.length + ' 条 · 最新信号已并入';
  }

  async function loadHistory(append, widenedLimit) {
    historySequence++;
    const current = historySequence;
    historyBusy = true;
    const params = new URLSearchParams({ symbol: $('alerts-symbol').value.trim(), kind: $('alerts-kind').value, limit: String(widenedLimit || $('alerts-limit').value) });
    if (append && historyCursor) params.set('cursor', historyCursor);
    if (!widenedLimit) $('alerts-history-status').textContent = '正在读取…';
    $('alerts-more').disabled = true;
    try {
      const response = await fetch(base + 'api/v1/alerts/signals?' + params, { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 401) { location.assign(base); return; }
      const packet = await response.json();
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      if (current !== historySequence) return;
      historyRows = append ? historyRows.concat(packet.rows || []) : (packet.rows || []);
      historyCursor = packet.next_cursor || '';
      signalRows(historyRows, '尚无信号');
      $('alerts-history-status').textContent = '激进 ' + packet.aggressive_count + ' · 稳健 ' + packet.steady_count + ' · 共 ' + packet.signal_count + ' 条 · 每 5 秒自动刷新';
      $('alerts-count').textContent = '已显示 ' + historyRows.length + ' 条';
      $('alerts-more').disabled = !historyCursor;
      clearError();
    } catch (caught) {
      if (current !== historySequence) return;
      error('信号历史读取失败：' + caught.message);
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
      telegram_chat_id: $('alerts-chat').value.trim()
    };
    const token = $('alerts-token').value.trim();
    if (token) body.telegram_token = token;
    return body;
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

  $('alerts-filters').addEventListener('submit', event => { event.preventDefault(); loadHistory(false); });
  $('alerts-more').addEventListener('click', () => loadHistory(true));
  $('alerts-settings').addEventListener('submit', saveSettings);
  $('alerts-test').addEventListener('click', testSettings);
  loadOverview();
  loadHistory(false);
  setInterval(() => { if (!document.hidden) loadOverview(); }, 5000);
})();

