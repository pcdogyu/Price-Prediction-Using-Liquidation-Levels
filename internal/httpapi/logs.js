'use strict';
(() => {
  const meta = document.querySelector('meta[name="app-base"]');
  const base = meta ? meta.content : '/';
  const rows = document.getElementById('logs-rows');
  const search = document.getElementById('logs-search');
  const level = document.getElementById('logs-level');
  const refresh = document.getElementById('logs-refresh');
  const first = document.getElementById('logs-first');
  const previous = document.getElementById('logs-prev');
  const next = document.getElementById('logs-next');
  const pageLabel = document.getElementById('logs-page');
  const status = document.getElementById('logs-status');
  const errorBox = document.getElementById('logs-error');
  const pageSize = 20;
  let page = 1;
  let pageCursors = [''];
  let nextBefore = '';
  let loading = false;
  let reloadPending = false;
  let searchTimer = 0;

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

  function formatFields(value) {
    if (value == null || value === '') return '-';
    let parsed = value;
    if (typeof value === 'string') {
      try { parsed = JSON.parse(value); } catch (_) { return value; }
    }
    if (parsed && typeof parsed === 'object' && !Object.keys(parsed).length) return '-';
    try { return JSON.stringify(parsed, null, 2); } catch (_) { return String(parsed); }
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
    pre.textContent = formatFields(entry.fields);
    fields.appendChild(pre);
    row.appendChild(fields);
    rows.appendChild(row);
  }

  function updatePagination() {
    pageLabel.textContent = '第 ' + page + ' 页';
    first.disabled = loading || page <= 1;
    previous.disabled = loading || page <= 1;
    next.disabled = loading || !nextBefore;
    refresh.disabled = loading;
    refresh.setAttribute('aria-busy', loading ? 'true' : 'false');
  }

  async function loadPage(targetPage) {
    if (loading) return;
    targetPage = Math.max(1, targetPage);
    loading = true;
    updatePagination();
    setError('');
    status.textContent = targetPage === 1 ? '正在刷新…' : '正在加载第 ' + targetPage + ' 页…';
    const params = new URLSearchParams({ limit: String(pageSize), level: level.value || 'DEBUG' });
    const query = search.value.trim();
    if (query) params.set('search', query);
    const before = pageCursors[targetPage - 1] || '';
    if (before) params.set('before', before);
    try {
      const response = await fetch(base + 'api/v1/logs?' + params.toString(), { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 401) { window.location.assign(base); return; }
      const data = await response.json();
      if (!response.ok) throw new Error(data.detail || ('HTTP ' + response.status));
      rows.replaceChildren();
      (Array.isArray(data.entries) ? data.entries : []).forEach(appendEntry);
      page = targetPage;
      nextBefore = data.next_before || '';
      pageCursors.length = targetPage;
      if (nextBefore) pageCursors[targetPage] = nextBefore;
      pageLabel.textContent = '第 ' + page + ' 页';
      status.textContent = (data.entries || []).length + ' 条 · 每页 ' + pageSize + ' 条 · 更新于 ' + formatTime(data.generated_at);
    } catch (err) {
      status.textContent = '加载失败';
      setError('日志加载失败：' + (err && err.message ? err.message : String(err)));
    } finally {
      loading = false;
      updatePagination();
      if (reloadPending) {
        reloadPending = false;
        loadPage(1);
      }
    }
  }

  function resetAndLoad() {
    page = 1;
    pageCursors = [''];
    nextBefore = '';
    if (loading) {
      reloadPending = true;
      return;
    }
    loadPage(1);
  }

  refresh.addEventListener('click', resetAndLoad);
  level.addEventListener('change', resetAndLoad);
  search.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      window.clearTimeout(searchTimer);
      resetAndLoad();
    }
  });
  search.addEventListener('input', () => {
    window.clearTimeout(searchTimer);
    searchTimer = window.setTimeout(resetAndLoad, 300);
  });
  first.addEventListener('click', () => loadPage(1));
  previous.addEventListener('click', () => loadPage(page - 1));
  next.addEventListener('click', () => loadPage(page + 1));
  document.getElementById('logs-close').addEventListener('click', () => {
    if (window.opener && !window.opener.closed) window.close();
    else if (window.history.length > 1) window.history.back();
    else window.location.assign(base);
  });
  updatePagination();
  loadPage(1);
})();
