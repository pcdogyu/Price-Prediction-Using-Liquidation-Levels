const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require.resolve('./theme.js'), 'utf8');

function bootTheme({ stored = 'light', remoteTheme = 'dark', fetchError = false, includeBaseMeta = true } = {}) {
  const calls = [];
  const local = new Map([['liquidation.theme', stored]]);
  const root = { dataset: {} };
  const window = {
    location: { href: 'https://example.test/liquidation/tvbot?tab=dashboard', pathname: '/liquidation/tvbot' },
    dispatchEvent() {},
    addEventListener() {}
  };
  const document = {
    documentElement: root,
    currentScript: { src: 'https://example.test/liquidation/assets/theme.js' },
    readyState: 'complete',
    querySelector(selector) {
      return includeBaseMeta && selector === 'meta[name="app-base"]' ? { content: '/liquidation/' } : null;
    },
    querySelectorAll() { return []; },
    addEventListener() {}
  };
  const context = {
    window,
    document,
    localStorage: {
      getItem(key) { return local.get(key) ?? null; },
      setItem(key, value) { local.set(key, value); }
    },
    CustomEvent: class CustomEvent { constructor(type, init) { this.type = type; this.detail = init?.detail; } },
    URL,
    Promise,
    fetch: async (url, options = {}) => {
      calls.push({ url, options });
      if (fetchError) throw new Error('offline');
      if (options.method === 'PUT') return { ok: true, json: async () => ({ theme: options.body }) };
      return { ok: true, json: async () => ({ theme: remoteTheme }) };
    }
  };
  vm.runInNewContext(source, context, { filename: 'theme.js' });
  return { calls, local, root, window, context };
}

const flush = () => new Promise(resolve => setImmediate(resolve));

test('server theme overrides local cache and saves later changes', async () => {
  const state = bootTheme({ stored: 'light', remoteTheme: 'dark' });
  await flush();
  assert.equal(state.root.dataset.theme, 'dark');
  assert.equal(state.local.get('liquidation.theme'), 'dark');
  assert.equal(state.calls[0].url, '/liquidation/api/v1/user/theme');
  assert.equal(state.calls[0].options.method, undefined);

  state.window.LiquidationTheme.set('light');
  await flush();
  const save = state.calls.at(-1);
  assert.equal(save.options.method, 'PUT');
  assert.deepEqual(JSON.parse(save.options.body), { theme: 'light' });
  assert.equal(state.root.dataset.theme, 'light');
  assert.equal(state.local.get('liquidation.theme'), 'light');
});

test('theme remains usable when server persistence is unavailable', async () => {
  const state = bootTheme({ stored: 'dark', fetchError: true });
  await flush();
  state.window.LiquidationTheme.set('light');
  await flush();
  assert.equal(state.root.dataset.theme, 'light');
  assert.equal(state.local.get('liquidation.theme'), 'light');
});

test('TVBot asset path resolves the prefixed theme endpoint without app-base metadata', async () => {
  const state = bootTheme({ includeBaseMeta: false, remoteTheme: 'light' });
  await flush();
  assert.equal(state.calls[0].url, '/liquidation/api/v1/user/theme');
  assert.equal(state.root.dataset.theme, 'light');
});
