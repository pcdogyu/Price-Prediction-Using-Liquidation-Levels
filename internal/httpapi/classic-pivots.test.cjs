const { test } = require('node:test');
const assert = require('node:assert/strict');
const { calculate } = require('./classic-pivots.js');

test('classic levels use the current price as P and ATR as the estimated range', () => {
  const levels = calculate(2495, 10);
  assert.deepEqual(levels, {
    r3: 2510, r2: 2505, r1: 2500, p: 2495,
    s1: 2490, s2: 2485, s3: 2480,
    atr: 10, high: 2500, low: 2490,
  });
});

test('classic levels reject unavailable or non-positive price and ATR', () => {
  for (const input of [[0, 10], [100, 0], [100, -1], [NaN, 10], [100, Infinity]]) {
    assert.equal(calculate(...input), null);
  }
});
