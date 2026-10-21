'use strict';

(function (root, factory) {
  const api = factory();
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (root) root.ClassicPivots = api;
})(typeof window !== 'undefined' ? window : globalThis, function () {
  const finite = value => typeof value === 'number' && Number.isFinite(value);
  const atrMultiples = Object.freeze([0.5, 1, 1.5, 2, 3]);

  // The dashboard has a live price and ATR(14), so show the distance ladder
  // symmetrically around that price instead of fabricating a prior session
  // high/low for floor-trader Classic formulas.
  function calculate(price, atr) {
    if (!finite(price) || price <= 0 || !finite(atr) || atr <= 0) return null;

    return {
      price,
      atr,
      levels: atrMultiples.map(multiplier => {
        const distance = atr * multiplier;
        return {
          multiplier,
          distance,
          upper: price + distance,
          lower: price - distance,
        };
      }),
    };
  }

  return { calculate, atrMultiples };
});
