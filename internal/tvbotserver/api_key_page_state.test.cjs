const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const ui = fs.readFileSync(__dirname + '/ui.go', 'utf8');
const helpers = ui.slice(ui.indexOf('    function readAPIKeyPageState()'), ui.indexOf('    async function loadBalanceOverview('));

test('API Key page saves only the selected exchange and environment and restores them on that page', async () => {
  let stored = null;
  let message = '';
  let route = 'apiKeys';
  const context = {
    window: { localStorage: {
      getItem: () => stored,
      setItem: (key, value) => { assert.equal(key, 'tvbot.apiKeyPage.v1'); stored = value; }
    } },
    apiKeyPageStorageKey: 'tvbot.apiKeyPage.v1',
    positionExchanges: ['okx', 'binance', 'gate'],
    state: { apiKeyExchange: 'gate', apiKeyEnv: 'live' },
    normalizeExchange: value => value,
    toast: value => { message = value; },
    configuredAPIKeyTarget: () => ({ exchange: 'okx', env: 'demo' }),
    requestedPositionEnvironment: () => '',
    tabFromRoute: () => route,
    loadAPIKeyStatus: async () => {},
    loadAPIKeys: async (exchange, env) => ({ exchange, env })
  };
  vm.createContext(context);
  vm.runInContext(helpers, context);
  context.saveAPIKeyPageState();
  assert.deepEqual(JSON.parse(stored), { exchange: 'gate', env: 'live' });
  assert.match(message, /页面状态已保存/);
  context.state.apiKeyExchange = 'okx'; context.state.apiKeyEnv = 'demo';
  let result = await context.loadInitialAPIKeys();
  assert.equal(result.exchange, 'gate'); assert.equal(result.env, 'live');
  route = 'positions';
  result = await context.loadInitialAPIKeys();
  assert.equal(result.exchange, 'okx'); assert.equal(result.env, 'demo');
  route = 'apiKeys'; stored = '{invalid';
  result = await context.loadInitialAPIKeys();
  assert.equal(result.exchange, 'okx'); assert.equal(result.env, 'demo');
  stored = JSON.stringify({exchange: 'bad', env: 'live'});
  assert.equal(context.readAPIKeyPageState(), null);
  context.window.localStorage.setItem = () => { throw Error('blocked'); };
  context.saveAPIKeyPageState();
  assert.match(message, /保存失败/);
});
