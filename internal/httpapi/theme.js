'use strict';
(() => {
  const root = document.documentElement;
  const storageKey = 'liquidation.theme';
  let saved = 'dark';
  try { saved = localStorage.getItem(storageKey) || saved; } catch (_) { /* storage is optional */ }
  root.dataset.theme = saved === 'light' ? 'light' : 'dark';
  let colors = {};
  const tokens = {
    '#07141d':'chart-bg', '#071019':'chart-label-bg', '#83a0b2':'muted',
    '#7895a7':'muted', '#9ab4c4':'muted', '#18313f':'chart-grid', '#213946':'line',
    '#315064':'strong-line', '#527083':'strong-line', '#f8fafc':'text',
    '#2dd4bf':'buy', '#fb7185':'sell', '#fbbf24':'warning', '#22c55e':'green',
    '#ef4444':'red', '#60a5fa':'blue', '#a855f7':'purple', '#c084fc':'purple-light',
    '#7dd3fc':'sky', '#6188ff':'indigo', '#f5c400':'gold', '#f08833':'orange',
    '#6ee7b7':'peak-buy', '#fda4af':'peak-sell'
  };
  function set(theme) {
    root.dataset.theme = theme === 'light' ? 'light' : 'dark';
    colors = {};
    try { localStorage.setItem(storageKey, root.dataset.theme); } catch (_) { /* keep in-memory selection */ }
    window.dispatchEvent(new CustomEvent('themechange', {detail: root.dataset.theme}));
  }
  window.LiquidationTheme = {
    set,
    toggle: () => set(root.dataset.theme === 'dark' ? 'light' : 'dark'),
    color(value) {
      if (value === 'none' || value === 'transparent' || value.startsWith('url(')) return value;
      if (value.startsWith('#') && !tokens[value]) return value;
      const name = tokens[value] || value;
      if (!colors[name]) colors[name] = getComputedStyle(root).getPropertyValue('--' + name).trim();
      return colors[name] || value;
    }
  };
  function setupSystemMenus() {
    document.querySelectorAll('[data-system-menu="true"]').forEach((menu) => {
      const trigger = menu.querySelector('.nav-system-trigger');
      const panel = menu.querySelector('.nav-system-panel');
      if (!trigger || !panel || menu.dataset.systemMenuReady === 'true') return;
      menu.dataset.systemMenuReady = 'true';
      const close = () => {
        panel.hidden = true;
        trigger.setAttribute('aria-expanded', 'false');
      };
      trigger.addEventListener('click', (event) => {
        event.stopPropagation();
        const open = panel.hidden;
        document.querySelectorAll('.nav-system-panel').forEach((other) => { other.hidden = true; });
        panel.hidden = !open;
        trigger.setAttribute('aria-expanded', open ? 'true' : 'false');
      });
      document.addEventListener('click', (event) => {
        if (!menu.contains(event.target)) close();
      });
      document.addEventListener('keydown', (event) => {
        if (event.key === 'Escape') close();
      });
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', setupSystemMenus, { once: true });
  } else {
    setupSystemMenus();
  }
})();
