'use strict';
(() => {
  const base = document.querySelector('meta[name="app-base"]').content;
  const dialog = document.getElementById('shared-logs');
  async function load() {
    const output = document.getElementById('shared-log-content');
    try {
      const level = document.getElementById('shared-log-level').value;
      const response = await fetch(base + 'api/v1/logs?limit=200&level=' + level, { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 401) { location.assign(base); return; }
      const data = await response.json();
      if (!response.ok) throw new Error(data.detail || response.status);
      output.textContent = (data.entries || []).map(row => JSON.stringify(row)).join('\n') || '暂无日志';
    } catch (error) { output.textContent = '加载失败：' + error.message; }
  }
  document.getElementById('nav-logs').addEventListener('click', () => { dialog.showModal(); load(); });
  document.getElementById('shared-logs-close').addEventListener('click', () => dialog.close());
  document.getElementById('shared-log-refresh').addEventListener('click', load);
  document.getElementById('shared-log-level').addEventListener('change', load);
})();
