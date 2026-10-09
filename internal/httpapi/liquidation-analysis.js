'use strict';
((root, factory) => {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.LiquidationAnalysis = factory();
})(typeof window === 'undefined' ? globalThis : window, () => {
  const labels = ['1H', '4H', '12H', '24H'];
  const names = {long:'多头清算占优', short:'空头清算占优', balanced:'多空均衡', empty:'暂无清算', missing:'数据缺失'};
  const markers = {long:'多', short:'空', balanced:'均衡', empty:'暂无', missing:'缺失'};
  const valid = value => typeof value === 'number' && Number.isFinite(value) && value >= 0;
  const interpretations = [
    ['多多多多', '各窗口均由多头爆仓主导，下行去杠杆从近期延伸至全天；卖压持续，但连续清算后也需防范超跌反抽。'],
    ['多多多空', '24H仍以空头爆仓为主，但1H至12H已转为多头爆仓；全天上冲结构正被持续下杀取代，短期偏弱。'],
    ['多多空多', '1H、4H与24H均为多头爆仓，12H曾短暂逼空；中段反弹未改变整体下行去杠杆，偏弱且容易反复。'],
    ['多多空空', '12H、24H仍体现上行逼空，1H、4H已转为多头爆仓；近期由上冲切换为下杀，属于偏空的短周期反转结构。'],
    ['多空多多', '大部分窗口为多头爆仓，仅4H出现空头爆仓；阶段反弹未扭转更广及最新的下行去杠杆，偏弱震荡。'],
    ['多空多空', '相邻窗口多空交替且最新1H为多头爆仓；双向扫损明显、结构噪声高，趋势可信度较低。'],
    ['多空空多', '4H、12H的逼空夹在24H下杀与最新1H回落之间；中期反弹可能衰竭，市场重新承受下行去杠杆。'],
    ['多空空空', '4H至24H均为空头爆仓，只有1H转为多头爆仓；持续上冲后出现最新回落，属于早期转弱或回调，仍需确认。'],
    ['空多多多', '4H至24H均为多头爆仓，只有1H转为空头爆仓；持续下杀后出现最新逼空，属于早期止跌或反弹，仍需确认。'],
    ['空多多空', '24H与最新1H为空头爆仓，4H、12H为多头爆仓；中期回撤后重新上冲，可能是下杀衰竭后的恢复，但结构仍反复。'],
    ['空多空多', '相邻窗口多空交替且最新1H为空头爆仓；双向扫损明显、结构噪声高，虽有上冲但趋势可信度较低。'],
    ['空多空空', '大部分窗口为空头爆仓，仅4H出现多头爆仓；阶段回撤未破坏更广及最新的上行逼空，偏强震荡。'],
    ['空空多多', '12H、24H仍体现下行去杠杆，1H、4H已转为空头爆仓；近期由下杀切换为上冲，属于偏多的短周期反转结构。'],
    ['空空多空', '1H、4H与24H均为空头爆仓，12H曾短暂下杀；中段回撤未改变整体上行逼空，偏强但容易反复。'],
    ['空空空多', '24H仍以多头爆仓为主，但1H至12H已转为空头爆仓；全天弱势结构正被持续上冲取代，短期偏强。'],
    ['空空空空', '各窗口均由空头爆仓主导，上行逼空从近期延伸至全天；买盘挤压持续，但连续逼空后也需防范冲高回落。']
  ].map(([combination, interpretation]) => ({combination, sides:[...combination], interpretation}));
  const interpretationByCombination = Object.fromEntries(interpretations.map(item => [item.combination, item.interpretation]));

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
    const combination = complete ? periods.map(p => p.marker).join('') : '';
    return {
      periods, complete,
      combination:combination || '尚未形成完整多空组合',
      interpretation:combination ? interpretationByCombination[combination] : '',
      description:complete ? describe(periods) : periods.map(p => `${p.label} ${p.state_label}`).join('；') + '，尚未形成完整多空组合。'
    };
  }
  return {analyze, interpretations};
});
