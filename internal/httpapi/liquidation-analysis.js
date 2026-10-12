'use strict';
((root, factory) => {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.LiquidationAnalysis = factory();
})(typeof window === 'undefined' ? globalThis : window, () => {
  const labels = ['5M', '15M', '1H', '4H', '12H', '24H'];
  const names = {long:'多头清算占优', short:'空头清算占优', balanced:'多空均衡', empty:'暂无清算', missing:'数据缺失'};
  // A liquidated short is forced to buy and is therefore a bullish (多) signal;
  // a liquidated long is forced to sell and is therefore a bearish (空) signal.
  const markers = {long:'空', short:'多', balanced:'均衡', empty:'暂无', missing:'缺失'};
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
      card_class:state === 'short' ? 'liq-long-dominant' : state === 'long' ? 'liq-short-dominant' : ''
    };
  }

  function structure(periods) {
    const states = periods.map(p => p.state);
    if (states.every(state => state === states[0])) return '六周期清算方向一致。';
    if (states[0] !== states[1]) return '5M 与 15M 清算方向不同，短窗口存在分歧。';
    if (states[1] !== states[2]) return '5M、15M 清算方向一致，与 1H 窗口不同。';
    return '5M、15M、1H 清算方向一致，较长窗口存在分歧。';
  }

  function describe(periods, explainFlow = false) {
    const parts = ['short', 'long'].map(state => {
      const selected = periods.filter(p => p.state === state).map(p => p.label);
      if (!selected.length) return '';
      const scope = selected.length === labels.length ? '六个周期均为' : selected.join('、');
      const flow = explainFlow ? (state === 'short' ? '，对应被迫买入（多）' : '，对应被迫卖出（空）') : '';
      return scope + names[state] + flow;
    }).filter(Boolean);
    return parts.join('；') + '。' + structure(periods);
  }

  function analyze(input) {
    const periods = labels.map(label => period(label, Array.isArray(input) ? input : []));
    const complete = periods.every(p => p.state === 'long' || p.state === 'short');
    const combination = complete ? periods.map(p => p.marker).join('') : '';
    const description = complete ? describe(periods) : periods.map(p => `${p.label} ${p.state_label}`).join('；') + '，尚未形成完整多空组合。';
    return {
      periods, complete,
      combination:combination || '尚未形成完整多空组合',
      interpretation:complete ? describe(periods, true) : description,
      description
    };
  }
  return {analyze};
});
