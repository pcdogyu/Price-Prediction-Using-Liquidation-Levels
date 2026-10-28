'use strict';
(() => {
  const meta = document.querySelector('meta[name="app-base"]');
  const base = meta ? meta.content : '/';
  const rows = document.getElementById('logs-rows');
  const level = document.getElementById('logs-level');
  const refresh = document.getElementById('logs-refresh');
  const more = document.getElementById('logs-more');
  const status = document.getElementById('logs-status');
  const errorBox = document.getElementById('logs-error');
  let nextBefore = '';
  let loading = false;

  function setError(message) {
    errorBox.textContent = message || '';
    errorBox.hidden = !message;
  }

  function formatTime(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value || '-');
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false
    }).format(date);
  }

  function addCell(row, value, className) {
    const cell = document.createElement('td');
    if (className) cell.className = className;
    cell.textContent = value == null || value === '' ? '-' : String(value);
    row.appendChild(cell);
    return cell;
  }

  function appendEntry(entry) {
    const row = document.createElement('tr');
    addCell(row, formatTime(entry.time), 'logs-time');
    const levelCell = addCell(row, entry.level || '-', 'logs-level logs-level-' + String(entry.level || '').toLowerCase());
    levelCell.textContent = entry.level || '-';
    addCell(row, entry.message || '-', 'logs-message');
    const fields = document.createElement('td');
    fields.className = 'logs-fields';
    const pre = document.createElement('pre');
    pre.textContent = JSON.stringify(entry.fields || {}, null, 2);
    fields.appendChild(pre);
    row.appendChild(fields);
    rows.appendChild(row);
  }

  async function load(older) {
    if (loading) return;
    loading = true;
    setError('');
    status.textContent = older ? '正在加载更早日志…' : '正在刷新…';
    const params = new URLSearchParams({ limit: '200', level: level.value || 'DEBUG' });
    if (older && nextBefore) params.set('before', nextBefore);
    try {
      const response = await fetch(base + 'api/v1/logs?' + params.toString(), { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 401) { window.location.assign(base); return; }
      const data = await response.json();
      if (!response.ok) throw new Error(data.detail || ('HTTP ' + response.status));
      if (!older) rows.replaceChildren();
      (Array.isArray(data.entries) ? data.entries : []).forEach(appendEntry);
      nextBefore = data.next_before || '';
      more.disabled = !nextBefore;
      status.textContent = (data.entries || []).length + ' 条 · 更新于 ' + formatTime(data.generated_at);
    } catch (err) {
      status.textContent = '加载失败';
      setError('日志加载失败：' + (err && err.message ? err.message : String(err)));
    } finally {
      loading = false;
    }
  }

  refresh.addEventListener('click', () => { nextBefore = ''; load(false); });
  level.addEventListener('change', () => { nextBefore = ''; load(false); });
  more.addEventListener('click', () => load(true));
  document.getElementById('logs-close').addEventListener('click', () => {
    if (window.opener && !window.opener.closed) window.close();
    else if (window.history.length > 1) window.history.back();
    else window.location.assign(base);
  });
  load(false);
})();
