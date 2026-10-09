'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {analyze, interpretations} = require('./liquidation-analysis.js');
const labels = ['1H', '4H', '12H', '24H'];
const fixture = pattern => [...pattern].map((side, i) => ({label:labels[i], long_usd:side === '多' ? 200 : 100, short_usd:side === '空' ? 200 : 100, count:3}));

const cases = [
  ['多多多多', '四个周期均为多头清算占优，清算结构一致。'],
  ['空空空空', '四个周期均为空头清算占优，清算结构一致。'],
  ['多空空空', '仅1H多头清算占优，4H、12H、24H空头清算占优，近期窗口出现分歧。'],
  ['空多多多', '仅1H空头清算占优，4H、12H、24H多头清算占优，近期窗口出现分歧。'],
  ['多多空空', '近1H、4H多头清算占优，12H、24H空头清算占优，近期窗口与较宽窗口结构不同。'],
  ['空空多多', '近1H、4H空头清算占优，12H、24H多头清算占优，近期窗口与较宽窗口结构不同。'],
  ['多多多空', '1H、4H、12H多头清算占优，仅24H空头清算占优，较宽窗口与其余周期不同。'],
  ['空空空多', '1H、4H、12H空头清算占优，仅24H多头清算占优，较宽窗口与其余周期不同。'],
  ['多空多多', '仅4H空头清算占优，1H、12H、24H多头清算占优，其余三个周期一致。'],
  ['空多空空', '仅4H多头清算占优，1H、12H、24H空头清算占优，其余三个周期一致。'],
  ['多多空多', '仅12H空头清算占优，1H、4H、24H多头清算占优，其余三个周期一致。'],
  ['空空多空', '仅12H多头清算占优，1H、4H、24H空头清算占优，其余三个周期一致。'],
  ['多空空多', '1H、24H多头清算占优，4H、12H空头清算占优，两个中间窗口与其余周期不同。'],
  ['空多多空', '1H、24H空头清算占优，4H、12H多头清算占优，两个中间窗口与其余周期不同。'],
  ['多空多空', '1H、12H多头清算占优，4H、24H空头清算占优，周期间交错分歧，缺少一致的清算结构。'],
  ['空多空多', '1H、12H空头清算占优，4H、24H多头清算占优，周期间交错分歧，缺少一致的清算结构。']
];
for (const [pattern, description] of cases) {
  test(pattern, () => {
    const result = analyze(fixture(pattern).reverse());
    assert.equal(result.complete, true);
    assert.equal(result.combination, pattern);
    assert.equal(result.description, description);
    assert.deepEqual(result.periods.map(p => p.label), labels);
    result.periods.forEach((p, i) => {
      assert.equal(p.state, pattern[i] === '多' ? 'long' : 'short');
      assert.equal(p.total, 300);
      assert.equal(p.long_share + p.short_share, 1);
    });
    assert.equal(result.interpretation, interpretations.find(item => item.combination === pattern).interpretation);
  });
}
test('all 16 combinations have one complete interpretation row', () => {
  assert.equal(interpretations.length, 16);
  assert.equal(new Set(interpretations.map(item => item.combination)).size, 16);
  assert.deepEqual(interpretations.map(item => item.combination), [...itertools('多空', 4)]);
  interpretations.forEach(item => {
    assert.deepEqual(item.sides, [...item.combination]);
    assert.match(item.interpretation, /[。；]/);
  });
  assert.match(interpretations.find(item => item.combination === '多多空空').interpretation, /偏空的短周期反转/);
});

function* itertools(characters, length, prefix = '') {
  if (prefix.length === length) { yield prefix; return; }
  for (const character of characters) yield* itertools(characters, length, prefix + character);
}
test('equal, empty and missing windows do not produce a directional combination', () => {
  const result = analyze([
    {label:'1H', long_usd:100, short_usd:100, count:2},
    {label:'4H', long_usd:0, short_usd:0, count:0},
    {label:'12H', long_usd:2, short_usd:1, count:2}
  ]);
  assert.deepEqual(result.periods.map(p => p.state), ['balanced', 'empty', 'long', 'missing']);
  assert.equal(result.complete, false);
  assert.equal(result.combination, '尚未形成完整多空组合');
  assert.equal(result.periods[0].long_share, .5);
  assert.equal(result.periods[1].long_share, null);
  assert.equal(result.periods[3].total, null);
  assert.match(result.description, /24H 数据缺失/);
});
test('single-sided amounts remain valid and small differences retain exact comparison', () => {
  const rows = fixture('多多多多');
  rows[0].short_usd = 0;
  rows[1].long_usd = 100.00001;
  const result = analyze(rows);
  assert.equal(result.periods[0].long_share, 1);
  assert.equal(result.periods[0].short_share, 0);
  assert.equal(result.periods[1].state, 'long');
});
test('invalid, negative, overflowing and duplicate amounts are missing, never directional', () => {
  for (const value of [undefined, null, '100', NaN, Infinity, -1]) {
    const rows = fixture('多多多多');
    rows[0].long_usd = value;
    assert.equal(analyze(rows).periods[0].state, 'missing');
  }
  const rows = fixture('多多多多');
  rows[0].long_usd = rows[0].short_usd = Number.MAX_VALUE;
  assert.equal(analyze(rows).periods[0].state, 'missing');
  assert.equal(analyze([...rows, rows[0]]).periods[0].state, 'missing');
  assert.equal(analyze(null).periods.every(p => p.state === 'missing'), true);
});
