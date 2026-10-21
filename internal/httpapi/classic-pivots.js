'use strict';

(function (root, factory) {
  const api = factory();
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (root) root.ClassicPivots = api;
})(typeof window !== 'undefined' ? window : globalThis, function () {
  const finite = value => typeof value === 'number' && Number.isFinite(value);

  // The dashboard has a live price and ATR(14), but not a completed previous
  // session high/low. Estimate that range symmetrically around the live price
  // while keeping the standard floor-trader Classic formulas intact.
  function calculate(price, atr) {
    if (!finite(price) || price <= 0 || !finite(atr) || atr <= 0) return null;
    const high = price + atr / 2;
    const low = price - atr / 2;
    const close = price;
    const pivot = (high + low + close) / 3;
    const range = high - low;
    return {
      r3: high + 2 * (pivot - low),
      r2: pivot + range,
      r1: 2 * pivot - low,
      p: pivot,
      s1: 2 * pivot - high,
      s2: pivot - range,
      s3: low - 2 * (high - pivot),
      atr,
      high,
      low,
    };
  }

  return { calculate };
});
