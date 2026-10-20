'use strict';
(() => {
  const baseMeta = document.querySelector('meta[name="app-base"]');
  const base = baseMeta ? baseMeta.content : '/';
  const dialog = document.getElementById('shared-logs');
  const themeButton = document.getElementById('nav-theme') || document.getElementById('tvbot-theme');
  function updateThemeButton() {
    if (!themeButton) return;
    const light = document.documentElement.dataset.theme === 'light';
    themeButton.textContent = light ? '深色主题' : '浅色主题';
    themeButton.setAttribute('aria-label', '当前' + (light ? '浅色' : '深色') + '主题，切换至' + (light ? '深色' : '浅色') + '主题');
  }
  if (themeButton) {
    themeButton.addEventListener('click', () => window.LiquidationTheme.toggle());
    window.addEventListener('themechange', updateThemeButton);
    updateThemeButton();
  }
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
  const logsButton = document.getElementById('nav-logs');
  const closeButton = document.getElementById('shared-logs-close');
  const refreshButton = document.getElementById('shared-log-refresh');
  const levelSelect = document.getElementById('shared-log-level');
  if (logsButton && dialog) logsButton.addEventListener('click', () => { dialog.showModal(); load(); });
  if (closeButton && dialog) closeButton.addEventListener('click', () => dialog.close());
  if (refreshButton) refreshButton.addEventListener('click', load);
  if (levelSelect) levelSelect.addEventListener('change', load);
})();
