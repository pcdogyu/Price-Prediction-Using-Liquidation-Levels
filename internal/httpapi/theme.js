'use strict';
(() => {
  const root = document.documentElement;
  const storageKey = 'liquidation.theme';
  const normalize = theme => theme === 'light' ? 'light' : 'dark';
  let saved = 'dark';
  try { saved = localStorage.getItem(storageKey) || saved; } catch (_) { /* storage is optional */ }
  let currentTheme = normalize(saved);
  let userChangedTheme = false;
  let saveQueue = Promise.resolve();
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
  function themeEndpoint() {
    const meta = document.querySelector('meta[name="app-base"]');
    let base = meta && meta.content ? meta.content.trim() : '';
    if (!base && document.currentScript && document.currentScript.src) {
      const scriptPath = new URL(document.currentScript.src, window.location.href).pathname;
      const marker = '/assets/theme.js';
      const markerAt = scriptPath.lastIndexOf(marker);
      if (markerAt >= 0) base = scriptPath.slice(0, markerAt + 1);
    }
    if (!base) {
      const path = window.location.pathname;
      const tvbotAt = path.indexOf('/tvbot/');
      base = tvbotAt >= 0 ? path.slice(0, tvbotAt + 1) : path.slice(0, path.lastIndexOf('/') + 1);
    }
    if (!base.endsWith('/')) base += '/';
    return base + 'api/v1/user/theme';
  }
  const themeURL = themeEndpoint();
  function apply(theme, remember) {
    currentTheme = normalize(theme);
    root.dataset.theme = currentTheme;
    colors = {};
    if (remember) {
      try { localStorage.setItem(storageKey, currentTheme); } catch (_) { /* keep in-memory selection */ }
    }
    window.dispatchEvent(new CustomEvent('themechange', {detail: currentTheme}));
  }
  function saveRemote(theme) {
    if (typeof fetch !== 'function') return;
    saveQueue = saveQueue.then(async () => {
      try {
        await fetch(themeURL, {
          method: 'PUT',
          credentials: 'same-origin',
          cache: 'no-store',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({theme})
        });
      } catch (_) { /* local theme remains usable when persistence is unavailable */ }
    });
  }
  function set(theme) {
    userChangedTheme = true;
    apply(theme, true);
    saveRemote(currentTheme);
  }
  async function loadRemote() {
    if (typeof fetch !== 'function') return;
    try {
      const response = await fetch(themeURL, {credentials: 'same-origin', cache: 'no-store'});
      if (!response.ok || userChangedTheme) return;
      const payload = await response.json();
      if (payload && (payload.theme === 'dark' || payload.theme === 'light')) apply(payload.theme, true);
    } catch (_) { /* local theme remains usable when persistence is unavailable */ }
  }
  apply(currentTheme, true);
  window.LiquidationTheme = {
    set,
    toggle: () => set(currentTheme === 'dark' ? 'light' : 'dark'),
    color(value) {
      if (value === 'none' || value === 'transparent' || value.startsWith('url(')) return value;
      if (value.startsWith('#') && !tokens[value]) return value;
      const name = tokens[value] || value;
      if (!colors[name]) colors[name] = getComputedStyle(root).getPropertyValue('--' + name).trim();
      return colors[name] || value;
    }
  };
  loadRemote();
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
