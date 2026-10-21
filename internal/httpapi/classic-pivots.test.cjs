const { test } = require('node:test');
const assert = require('node:assert/strict');
const { calculate } = require('./classic-pivots.js');

test('ATR levels use the current price as the center of both directions', () => {
  const levels = calculate(2495, 10);
  assert.deepEqual(levels, {
    price: 2495,
    atr: 10,
    levels: [
      { multiplier: 0.5, distance: 5, upper: 2500, lower: 2490 },
      { multiplier: 1, distance: 10, upper: 2505, lower: 2485 },
      { multiplier: 1.5, distance: 15, upper: 2510, lower: 2480 },
      { multiplier: 2, distance: 20, upper: 2515, lower: 2475 },
      { multiplier: 3, distance: 30, upper: 2525, lower: 2465 },
    ],
  });
});

test('classic levels reject unavailable or non-positive price and ATR', () => {
  for (const input of [[0, 10], [100, 0], [100, -1], [NaN, 10], [100, Infinity]]) {
    assert.equal(calculate(...input), null);
  }
});
