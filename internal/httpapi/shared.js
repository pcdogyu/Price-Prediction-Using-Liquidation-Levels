'use strict';
(() => {
  const baseMeta = document.querySelector('meta[name="app-base"]');
  const base = baseMeta ? baseMeta.content : '/';
  const themeButton = document.getElementById('nav-theme') || document.getElementById('tvbot-theme');
  function updateThemeButton() {
    if (!themeButton) return;
    const light = document.documentElement.dataset.theme === 'light';
    themeButton.textContent = light ? '深色主题' : '浅色主题';
    themeButton.setAttribute('aria-label', '当前' + (light ? '浅色' : '深色') + '主题，切换至' + (light ? '深色' : '浅色') + '主题');
  }
  if (themeButton) {
    window.addEventListener('themechange', updateThemeButton);
    updateThemeButton();
  }
  const logsButton = document.getElementById('nav-logs');
  if (logsButton) logsButton.addEventListener('click', () => {
    const popup = window.open(base + 'logs', 'program-logs', 'popup=yes,width=1280,height=800,resizable=yes,scrollbars=yes');
    if (popup) {
      popup.focus();
    } else {
      window.location.assign(base + 'logs');
    }
  });

  const positionLinks = document.querySelectorAll('a[data-position-link]');
  if (positionLinks.length) {
    fetch(base + 'tvbot/position-target', { cache: 'no-store', credentials: 'same-origin' })
      .then((response) => response.ok ? response.json() : null)
      .then((target) => {
        const exchange = target && typeof target.exchange === 'string' ? target.exchange.trim().toLowerCase() : '';
        const env = target && typeof target.env === 'string' ? target.env.trim().toLowerCase() : '';
        if (!target || target.unique !== true || !['okx', 'binance', 'gate'].includes(exchange) || !['demo', 'live'].includes(env)) return;
        positionLinks.forEach((link) => {
          const url = new URL(link.getAttribute('href'), window.location.origin);
          url.searchParams.set('exchange', exchange);
          url.searchParams.set('env', env);
          link.setAttribute('href', url.pathname + url.search + url.hash);
        });
      })
      .catch(() => {});
  }
})();
