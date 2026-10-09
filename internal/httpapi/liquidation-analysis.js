'use strict';
((root, factory) => {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.LiquidationAnalysis = factory();
})(typeof window === 'undefined' ? globalThis : window, () => {
  const labels = ['1H', '4H', '12H', '24H'];
  const names = {long:'多头清算占优', short:'空头清算占优', balanced:'多空均衡', empty:'暂无清算', missing:'数据缺失'};
  const markers = {long:'多', short:'空', balanced:'均衡', empty:'暂无', missing:'缺失'};
  const valid = value => typeof value === 'number' && Number.isFinite(value) && value >= 0;

  function period(label, periods) {
    const matches = periods.filter(p => p?.label === label);
    const p = matches.length === 1 ? matches[0] : null;
    const usable = p && valid(p.long_usd) && valid(p.short_usd) && Number.isFinite(p.long_usd + p.short_usd);
    const total = usable ? p.long_usd + p.short_usd : null;
    const state = !usable ? 'missing' : total === 0 ? 'empty' : p.long_usd === p.short_usd ? 'balanced' : p.long_usd > p.short_usd ? 'long' : 'short';
    return {
      label, state, state_label:names[state], marker:markers[state], total,
      long_usd:usable ? p.long_usd : null, short_usd:usable ? p.short_usd : null,
      long_share:total > 0 ? p.long_usd / total : null,
      short_share:total > 0 ? p.short_usd / total : null,
      count:usable && Number.isInteger(p.count) && p.count >= 0 ? p.count : null,
      card_class:state === 'long' ? 'liq-long-dominant' : state === 'short' ? 'liq-short-dominant' : ''
    };
  }

  function describe(periods) {
    const [a, b, c, d] = periods.map(p => p.state);
    const side = state => state === 'long' ? '多头清算占优' : '空头清算占优';
    if (a === b && b === c && c === d) return `四个周期均为${side(a)}，清算结构一致。`;
    if (b === c && c === d) return `仅1H${side(a)}，4H、12H、24H${side(b)}，近期窗口出现分歧。`;
    if (a === b && c === d) return `近1H、4H${side(a)}，12H、24H${side(c)}，近期窗口与较宽窗口结构不同。`;
    if (a === b && b === c) return `1H、4H、12H${side(a)}，仅24H${side(d)}，较宽窗口与其余周期不同。`;
    if (a === c && c === d) return `仅4H${side(b)}，1H、12H、24H${side(a)}，其余三个周期一致。`;
    if (a === b && b === d) return `仅12H${side(c)}，1H、4H、24H${side(a)}，其余三个周期一致。`;
    if (a === d && b === c) return `1H、24H${side(a)}，4H、12H${side(b)}，两个中间窗口与其余周期不同。`;
    return `1H、12H${side(a)}，4H、24H${side(b)}，周期间交错分歧，缺少一致的清算结构。`;
  }

  function analyze(input) {
    const periods = labels.map(label => period(label, Array.isArray(input) ? input : []));
    const complete = periods.every(p => p.state === 'long' || p.state === 'short');
    return {
      periods, complete,
      combination:complete ? periods.map(p => p.marker).join('') : '尚未形成完整多空组合',
      description:complete ? describe(periods) : periods.map(p => `${p.label} ${p.state_label}`).join('；') + '，尚未形成完整多空组合。'
    };
  }
  return {analyze};
});
