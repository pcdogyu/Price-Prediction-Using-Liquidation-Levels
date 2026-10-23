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
  let alertGamma = { anchor_symbol: 'ETHUSDT', state: 'unavailable', ignored: false };
  const gammaValue = gamma => gamma && Number.isFinite(Number(gamma.gamma)) ? Number(gamma.gamma) : null;
  const gammaIgnored = gamma => !!(gamma && gamma.ignored);
  const historicalGammaIgnored = row => !!(row && row.gamma_ignored);
  const directionLabel = (side, ignored) => {
    if (ignored) return side === 'long' ? '做多 gamma忽略' : side === 'short' ? '做空 gamma 忽略' : '—';
    return sideLabel(side);
  };
  const directionClass = (side, ignored) => ignored && (side === 'long' || side === 'short') ? ' class="gamma-ignored"' : '';
  const filterReasonLabels = {
    peak_decay_armed: 'ARMED，等待爆仓脉冲衰竭',
    peak_decay_not_confirmed: '爆仓速率尚未降至峰值 40% 以下',
    price_absorption_not_confirmed: '价格尚未止跌吸收',
    market_context_unavailable: '市场 K 线数据未就绪',
    vwap_zscore_unavailable: 'VWAP Z-Score 不可用',
    donchian_range_unavailable_long_liquidation_close_only: 'Donchian 大周期数据不足：多单爆仓仅允许平仓',
    donchian_range_unavailable_short_liquidation_close_only: 'Donchian 大周期数据不足：空单爆仓仅允许平仓',
    vwap_donchian_zone_overlap_long_liquidation_close_only: 'VWAP/Donchian 区域重叠：多单爆仓仅允许平仓',
    vwap_donchian_zone_overlap_short_liquidation_close_only: 'VWAP/Donchian 区域重叠：空单爆仓仅允许平仓',
    high_zscore_long_liquidation_close_only: '高位区：多单爆仓仅允许平空',
    low_zscore_long_liquidation_open_long: '低位区：允许接多',
    neutral_zscore_long_liquidation_close_only: '中性区：多单爆仓仅允许平空',
    high_zscore_short_liquidation_open_short: '高位区：允许开空',
    low_zscore_short_liquidation_close_only: '低位区：空单爆仓仅允许平多',
    neutral_zscore_short_liquidation_close_only: '中性区：空单爆仓仅允许平多',
    deep_liquidation_quota_or_atr_not_ready: '深度爆仓配额或 ATR 回撤未达标',
    exponential_long_grid_not_ready: '多单指数网格间距未达标',
    unsupported_liquidation_side: '不支持的爆仓方向'
  };
  const radarFilterStatus = row => {
    if (!row || !row.filter_applied) return '';
    const raw = String(row.filter_reason || '').trim();
    const reasons = raw.split(';').map(value => value.trim()).filter(Boolean)
      .map(value => filterReasonLabels[value] || value);
    const phase = String(row.phase || '').trim();
    const permission = row.open_allowed && row.close_allowed ? '开仓+平仓' : row.open_allowed ? '开仓' : row.close_allowed ? '仅平仓' : '未放行';
    const summary = ['雷达', phase, permission, ...reasons].filter(Boolean).join(' · ');
    const title = raw || summary;
    return '<span class="muted alerts-radar-filter" title="' + escape(title) + '"> · ' + escape(summary) + '</span>';
  };
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

  const SIGNAL_RECALCULATE_STORAGE = 'liquidation.alerts.signal-recalculate';
  let overviewSequence = 0, historySequence = 0, historyRows = [], historyBusy = false, historyPage = 0, historyCursors = [''], historyNextCursor = '', historyReplayed = (() => { try { return !!localStorage.getItem(SIGNAL_RECALCULATE_STORAGE); } catch (_) { return false; } })(), settingsLoaded = false, orderSignalSettingsLoaded = false, strategyLoaded = false, strategySaveBusy = false, strategySavePending = false, strategySavePendingAuto = false, strategyRecalculatePending = false, signalRecalculateBusy = false, signalProgressTimer = 0, signalProgressValue = 1;

  function error(message) { const node = $('alerts-error'); node.classList.remove('success'); node.textContent = message; node.hidden = false; }
  function success(message) { const node = $('alerts-error'); node.classList.add('success'); node.innerHTML = message; node.hidden = false; }
  function clearError() { const node = $('alerts-error'); node.classList.remove('success'); node.hidden = true; }

  // The recalculation endpoint currently returns one response after all pairs
  // finish. Animate the client-side waiting state to 99%, then reserve 100%
  // for the confirmed successful response.
  function setSignalProgress(value, label) {
    signalProgressValue = Math.max(1, Math.min(100, Math.round(value)));
    const panel = $('alerts-signal-progress');
    const fill = $('alerts-signal-progress-fill');
    const output = $('alerts-signal-progress-value');
    const track = $('alerts-signal-progress-track');
    const button = $('alerts-signal-recalculate');
    panel.hidden = false;
    panel.classList.toggle('is-complete', signalProgressValue === 100);
    panel.classList.toggle('is-error', false);
    fill.style.width = signalProgressValue + '%';
    output.textContent = signalProgressValue + '%';
    track.setAttribute('aria-valuenow', String(signalProgressValue));
    if (label) $('alerts-signal-progress-label').textContent = label;
    if (signalRecalculateBusy) button.textContent = '重算中 ' + signalProgressValue + '%';
  }

  function startSignalProgress() {
    if (signalProgressTimer) window.clearInterval(signalProgressTimer);
    const startedAt = performance.now();
    setSignalProgress(1, '正在准备过去 48 小时信号…');
    signalProgressTimer = window.setInterval(() => {
      const elapsed = performance.now() - startedAt;
      const progress = Math.min(99, 1 + Math.floor(98 * (1 - Math.exp(-elapsed / 9000))));
      const label = progress < 35 ? '正在读取爆仓脉冲…' : progress < 75 ? '正在回放 Peak-Decay…' : '正在确认衰竭与吸收…';
      setSignalProgress(progress, label);
      if (progress >= 99) {
        window.clearInterval(signalProgressTimer);
        signalProgressTimer = 0;
      }
    }, 180);
  }

  function finishSignalProgress(successful) {
    if (signalProgressTimer) window.clearInterval(signalProgressTimer);
    signalProgressTimer = 0;
    if (successful) {
      setSignalProgress(100, '过去 48 小时信号重算完成');
      return;
    }
    const panel = $('alerts-signal-progress');
    panel.hidden = false;
    panel.classList.remove('is-complete');
    panel.classList.add('is-error');
    $('alerts-signal-progress-label').textContent = '信号重算失败';
  }

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
      '<td' + directionClass(signal.side, historicalGammaIgnored(signal)) + '>' + escape(directionLabel(signal.side, historicalGammaIgnored(signal))) + '<span class="muted"> · ' + escape(liqLabel(signal.liquidation_side)) + '</span></td>' +
      '<td>' + money(signal.alert_notional_usd) + '<span class="muted"> / ' + money(signal.threshold_usd) + '</span></td>' +
      '<td>' + detail + radarFilterStatus(signal) + '</td><td>' + price(signal.reference_price) + '</td><td>' + triggerLink(signal.kind, signal.id, signal.symbol, signal.side, signal.reference_price) + '</td><td>' + notifyLink(signal.kind, signal.id) + '</td>' +
      '<td>' + escape(notifyLabels[signal.notify_status] || signal.notify_status || '未推送') + (signal.notify_error ? '<span class="muted" title="' + escape(signal.notify_error) + '"> ⓘ</span>' : '') + '</td>';
  }

  function alertCells(row) {
    const detail = escape(row.event_count || 0) + ' 笔 · 顺延 ' + escape(row.follow_ups || 0) + ' 次 · ' +
      '<span class="' + stateClass(row.status) + '">' + escape(cycleStates[row.status] || row.status) + '</span>' +
      (row.quiet_until ? '<span class="muted">安静至 ' + when(row.quiet_until) + '</span>' : '');
    return '<td>' + when(row.triggered_at) + '</td><td>' + chartLink(row.symbol) + '</td>' +
      '<td class="warning">预警</td>' +
      '<td' + directionClass(row.trade_side, historicalGammaIgnored(row)) + '>' + escape(directionLabel(row.trade_side, historicalGammaIgnored(row))) + '<span class="muted"> · ' + escape(liqLabel(row.side)) + '</span></td>' +
      '<td>' + money(row.notional_usd) + '<span class="muted"> / ' + money(row.threshold_usd) + '</span></td>' +
      '<td>' + detail + radarFilterStatus(row) + '</td><td>' + price(row.reference_price) + '</td><td>' + triggerLink('alert', row.id, row.symbol, row.trade_side, row.reference_price) + '</td><td>' + notifyLink('alert', row.id) + '</td><td>—</td>';
  }

  function timelineRows(rows) {
    const target = $('alerts-history-rows');
    if (!rows.length) { target.innerHTML = '<tr><td colspan="10">尚无历史记录</td></tr>'; return; }
    target.innerHTML = rows.map(row => '<tr>' + (row.type === 'alert' ? alertCells(row.alert) : signalCells(row.signal)) + '</tr>').join('');
  }

  function renderGammaStatus(gamma) {
    alertGamma = gamma || { anchor_symbol: 'ETHUSDT', state: 'unavailable', ignored: false };
    const value = gammaValue(alertGamma);
    const valueText = value === null ? '值不可用' : value.toFixed(6);
    const anchor = alertGamma.anchor_symbol || 'ETHUSDT';
    let text = anchor + ' Gamma：' + valueText;
    if (alertGamma.state === 'stale') text += ' · 数据已过期，暂不抑制开仓';
    else if (gammaIgnored(alertGamma)) text += ' · < 0，预警 / 激进 / 稳健只发送平仓，方向显示 gamma 忽略';
    else if (alertGamma.state === 'ok' || alertGamma.state === 'partial') text += ' · 正常，沿用下单类别开关';
    else text += ' · 暂无可用检测，沿用下单类别开关';
    $('alerts-gamma-status').textContent = text;
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
      [$('alerts-order-alert-close').checked, '预警平仓'],
      [$('alerts-order-alert').checked, '预警开仓'],
      [$('alerts-order-aggressive-close').checked, '激进平仓'],
      [$('alerts-order-aggressive').checked, '激进开仓'],
      [$('alerts-order-steady-close').checked, '稳健平仓'],
      [$('alerts-order-steady').checked, '稳健开仓']
    ].filter(item => item[0]).map(item => item[1]);
    $('alerts-order-signal-summary').textContent = selected.length ? '已开启：' + selected.join('、') : '当前不自动发送下单信号';
  }

  function renderOrderSignalSettings(settings) {
    if (!settings) return;
    if (!orderSignalSettingsLoaded) {
      $('alerts-order-alert-close').checked = !!settings.order_alerts_close;
      $('alerts-order-alert').checked = !!settings.order_alerts;
      $('alerts-order-aggressive-close').checked = !!settings.order_aggressive_close;
      $('alerts-order-aggressive').checked = !!settings.order_aggressive;
      $('alerts-order-steady-close').checked = !!settings.order_steady_close;
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
    const fields = [['alerts-threshold-btc', settings.btc_usd], ['alerts-threshold-eth', settings.eth_usd], ['alerts-threshold-other', settings.other_usd], ['alerts-window-minutes', settings.window_minutes], ['alerts-vwap-range-hours', settings.vwap_range_hours], ['alerts-vwap-min-drawdown', settings.vwap_min_drawdown_pct], ['alerts-vwap-donchian', settings.vwap_donchian_pct]];
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
      renderGammaStatus(data.gamma);
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


  async function loadHistory(cursor, page, replay = historyReplayed) {
    historySequence++;
    const current = historySequence;
    historyBusy = true;
    const params = new URLSearchParams({ symbol: $('alerts-symbol').value.trim(), kind: $('alerts-kind').value });
    if (cursor) params.set('cursor', cursor);
    if (replay) params.set('replay', '1');
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
      historyReplayed = !!packet.replayed;
      renderGammaStatus(packet.gamma);
      historyPage = page;
      historyNextCursor = packet.next_cursor || '';
      timelineRows(historyRows);
      const sourceLabel = packet.replayed ? '过去 48 小时按当前参数重算 · ' : '';
      $('alerts-history-status').textContent = sourceLabel + '预警 ' + packet.alert_count + ' · 激进 ' + packet.aggressive_count + ' · 稳健 ' + packet.steady_count + ' · 共 ' + packet.total_count + ' 条';
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
      order_alerts: $('alerts-order-alert').checked,
      order_aggressive: $('alerts-order-aggressive').checked,
      order_steady: $('alerts-order-steady').checked,
      order_alerts_close: $('alerts-order-alert-close').checked,
      order_aggressive_close: $('alerts-order-aggressive-close').checked,
      order_steady_close: $('alerts-order-steady-close').checked,
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
      window_minutes: Number($('alerts-window-minutes').value),
      vwap_range_hours: Number($('alerts-vwap-range-hours').value),
      vwap_min_drawdown_pct: Number($('alerts-vwap-min-drawdown').value),
      vwap_donchian_pct: Number($('alerts-vwap-donchian').value)
    };
  }

  function orderSignalSettingsBody() {
    return {
      order_alerts: $('alerts-order-alert').checked,
      order_aggressive: $('alerts-order-aggressive').checked,
      order_steady: $('alerts-order-steady').checked,
      order_alerts_close: $('alerts-order-alert-close').checked,
      order_aggressive_close: $('alerts-order-aggressive-close').checked,
      order_steady_close: $('alerts-order-steady-close').checked
    };
  }

  async function saveStrategySettings(event, automatic = false) {
    if (event) event.preventDefault();
    if (automatic && !strategyLoaded) return;
    if (strategySaveBusy) {
      strategySavePending = true;
      strategySavePendingAuto = strategySavePendingAuto || automatic;
      return;
    }
    strategySaveBusy = true;
    $('alerts-strategy-save').disabled = true;
    $('alerts-strategy-status').textContent = automatic ? '正在自动保存报警参数…' : '正在保存报警参数…';
    try {
      const response = await fetch(base + 'api/v1/alerts/strategy-settings', { method: 'POST', cache: 'no-store', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(strategySettingsBody()) });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      renderStrategySettings(packet);
      $('alerts-strategy-status').textContent = automatic ? '报警参数已自动保存并立即生效' : '报警参数已保存并立即生效';
      clearError();
      loadOverview();
    } catch (caught) {
      error('保存报警参数失败：' + caught.message);
      $('alerts-strategy-status').textContent = '保存失败';
    } finally {
      strategySaveBusy = false;
      $('alerts-strategy-save').disabled = false;
      if (strategySavePending) {
        const pendingAutomatic = strategySavePendingAuto;
        strategySavePending = false;
        strategySavePendingAuto = false;
        void saveStrategySettings(null, pendingAutomatic);
      } else if (strategyRecalculatePending) {
        strategyRecalculatePending = false;
        void recalculateSignals();
      }
    }
  }

  async function recalculateSignals() {
    if (strategySaveBusy) {
      strategyRecalculatePending = true;
      $('alerts-strategy-status').textContent = '等待报警参数保存完成后重算…';
      return;
    }
    if (signalRecalculateBusy) return;
    const button = $('alerts-signal-recalculate');
    signalRecalculateBusy = true;
    button.disabled = true;
    startSignalProgress();
    $('alerts-strategy-status').textContent = '正在重算过去 48 小时信号…';
    try {
      const response = await fetch(base + 'api/v1/alerts/recalculate', { method: 'POST', cache: 'no-store', credentials: 'same-origin' });
      const packet = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(packet.detail || 'HTTP ' + response.status);
      const recalculatedAt = packet.recalculated_at || new Date().toISOString();
      try { localStorage.setItem(SIGNAL_RECALCULATE_STORAGE, recalculatedAt); } catch (_) { /* private mode still refreshes this page */ }
      const symbols = Number(packet.symbol_count) || 0;
      const alerts = Number(packet.alert_count) || 0;
      const signals = Number(packet.signal_count) || 0;
      $('alerts-strategy-status').textContent = '过去 48 小时已重算 · ' + symbols + ' 个币对 · 预警 ' + alerts + ' · 信号 ' + signals;
      finishSignalProgress(true);
      clearError();
      loadOverview();
      historyCursors = [''];
      void loadHistory('', 0, true);
    } catch (caught) {
      error('信号重算失败：' + caught.message);
      finishSignalProgress(false);
      $('alerts-strategy-status').textContent = '信号重算失败';
    } finally {
      signalRecalculateBusy = false;
      button.disabled = false;
      button.textContent = '信号重算';
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
      const exchange = String(packet.target_exchange || '').toLowerCase();
      const exchangeLabel = exchange === 'gate' ? 'Gate' : exchange === 'okx' ? 'OKX' : exchange === 'binance' ? 'Binance' : '配置目标交易所';
      const envLabel = packet.trade_env === 'live' ? '实盘' : '模拟盘';
      const ordersURL = base + 'tvbot/view/orders' + (exchange ? '?exchange=' + encodeURIComponent(exchange) : '');
      success('下单信号已发送到 ' + escape(exchangeLabel) + ' ' + escape(envLabel) + '。订单历史按交易所分开，请刷新对应页面查看。 <a href="' + escape(ordersURL) + '">打开订单历史</a>');
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

  $('alerts-filters').addEventListener('submit', event => { event.preventDefault(); historyCursors = ['']; loadHistory('', 0, historyReplayed); });
  $('alerts-prev').addEventListener('click', () => { if (historyPage > 0) loadHistory(historyCursors[historyPage - 1], historyPage - 1, historyReplayed); });
  $('alerts-next').addEventListener('click', () => { if (!historyNextCursor) return; historyCursors[historyPage + 1] = historyNextCursor; loadHistory(historyNextCursor, historyPage + 1, historyReplayed); });
  $('alerts-settings').addEventListener('submit', saveSettings);
  $('alerts-strategy-settings').addEventListener('submit', saveStrategySettings);
  ['alerts-threshold-btc', 'alerts-threshold-eth', 'alerts-threshold-other', 'alerts-window-minutes', 'alerts-vwap-range-hours', 'alerts-vwap-min-drawdown', 'alerts-vwap-donchian'].forEach(id => $(id).addEventListener('blur', () => { void saveStrategySettings(null, true); }));
  $('alerts-signal-recalculate').addEventListener('click', recalculateSignals);
  $('alerts-order-signal-settings').addEventListener('submit', saveOrderSignalSettings);
  ['alerts-order-alert-close', 'alerts-order-alert', 'alerts-order-aggressive-close', 'alerts-order-aggressive', 'alerts-order-steady-close', 'alerts-order-steady'].forEach(id => $(id).addEventListener('change', renderOrderSignalSummary));
  $('alerts-test').addEventListener('click', testSettings);
  $('alerts-history-rows').addEventListener('click', event => {
    const button = event.target.closest('.alerts-action');
    if (!button) return;
    if (button.dataset.action === 'notify') triggerNotify(button);
    else triggerOrder(button);
  });
  loadOverview();
  loadHistory('', 0, historyReplayed);
  setInterval(() => { if (!document.hidden) { loadOverview(); if (historyPage === 0 && !historyBusy) loadHistory('', 0, historyReplayed); } }, 5000);
})();

