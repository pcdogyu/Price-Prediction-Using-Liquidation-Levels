'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {analyze} = require('./liquidation-analysis.js');
const labels = ['5M', '15M', '1H', '4H', '12H', '24H'];
const fixture = pattern => [...pattern].map((side, i) => ({label:labels[i], long_usd:side === '空' ? 200 : 100, short_usd:side === '多' ? 200 : 100, count:3}));

function* combinations(prefix = '') {
  if (prefix.length === 6) { yield prefix; return; }
  for (const side of '多空') yield* combinations(prefix + side);
}
for (const pattern of combinations()) {
  test('six-period state ' + pattern, () => {
    const result = analyze(fixture(pattern).reverse());
    assert.equal(result.complete, true);
    assert.equal(result.combination, pattern);
    assert.deepEqual(result.periods.map(p => p.label), labels);
    result.periods.forEach((p, i) => {
      assert.equal(p.marker, pattern[i]);
      assert.equal(p.state, pattern[i] === '多' ? 'short' : 'long');
      assert.equal(p.card_class, pattern[i] === '多' ? 'liq-long-dominant' : 'liq-short-dominant');
      assert.equal(p.total, 300);
      assert.equal(p.long_share + p.short_share, 1);
      if (new Set(pattern).size > 1) assert.ok(result.description.includes(labels[i]));
    });
    if (pattern.includes('多')) assert.match(result.interpretation, /空头清算占优，对应被迫买入（多）/);
    if (pattern.includes('空')) assert.match(result.interpretation, /多头清算占优，对应被迫卖出（空）/);
    assert.doesNotMatch(result.description + result.interpretation, /NaN|Infinity|趋势反转|止跌|偏强|偏弱/);
  });
}
test('short windows distinguish agreement and disagreement across six periods', () => {
  assert.match(analyze(fixture('多空多多多多')).description, /5M 与 15M 清算方向不同/);
  assert.match(analyze(fixture('多多空空空空')).description, /5M、15M 清算方向一致，与 1H 窗口不同/);
  assert.match(analyze(fixture('多多多空空空')).description, /5M、15M、1H 清算方向一致，较长窗口存在分歧/);
  assert.equal(analyze(fixture('多多多多多多')).description, '六个周期均为空头清算占优。六周期清算方向一致。');
  assert.equal(analyze(fixture('空空空空空空')).description, '六个周期均为多头清算占优。六周期清算方向一致。');
});
test('missing 5M or 15M cannot fall back to the old four-period state', () => {
  for (const index of [0, 1]) {
    const rows = fixture('多多多多多多');
    rows.splice(index, 1);
    const result = analyze(rows);
    assert.equal(result.complete, false);
    assert.equal(result.periods[index].state, 'missing');
  }
});
test('equal, empty and missing windows do not produce a directional combination', () => {
  const result = analyze([
    {label:'5M', long_usd:100, short_usd:100, count:2},
    {label:'15M', long_usd:0, short_usd:0, count:0},
    {label:'1H', long_usd:2, short_usd:1, count:2}
  ]);
  assert.deepEqual(result.periods.map(p => p.state), ['balanced', 'empty', 'long', 'missing', 'missing', 'missing']);
  assert.equal(result.interpretation, result.description);
  assert.deepEqual(result.periods.map(p => p.marker), ['均衡', '暂无', '空', '缺失', '缺失', '缺失']);
  assert.equal(result.complete, false);
  assert.equal(result.combination, '尚未形成完整多空组合');
  assert.equal(result.periods[0].long_share, .5);
  assert.equal(result.periods[1].long_share, null);
  assert.equal(result.periods[3].total, null);
  assert.match(result.description, /24H 数据缺失/);
});
test('single-sided amounts remain valid and small differences retain exact comparison', () => {
  const rows = fixture('多多多多多多');
  rows[0].short_usd = 0;
  rows[1].long_usd = rows[1].short_usd + .00001;
  const result = analyze(rows);
  assert.equal(result.periods[0].long_share, 1);
  assert.equal(result.periods[0].short_share, 0);
  assert.equal(result.periods[1].state, 'long');
});
test('invalid, negative, overflowing and duplicate amounts are missing, never directional', () => {
  for (const value of [undefined, null, '100', NaN, Infinity, -1]) {
    const rows = fixture('多多多多多多');
    rows[0].long_usd = value;
    assert.equal(analyze(rows).periods[0].state, 'missing');
  }
  const rows = fixture('多多多多多多');
  rows[0].long_usd = rows[0].short_usd = Number.MAX_VALUE;
  assert.equal(analyze(rows).periods[0].state, 'missing');
  assert.equal(analyze([...rows, rows[0]]).periods[0].state, 'missing');
  assert.equal(analyze(null).periods.every(p => p.state === 'missing'), true);
});
