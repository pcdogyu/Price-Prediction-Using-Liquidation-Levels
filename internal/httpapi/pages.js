'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const base = document.querySelector('meta[name="app-base"]').content;
  const page = document.body.dataset.page;
  const paint = value => window.LiquidationTheme.color(value);
  const valid = value => typeof value === 'number' && Number.isFinite(value);
  const escape = value => String(value ?? '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
  const amount = value => valid(value) ? value.toLocaleString('zh-CN', {maximumFractionDigits: 2}) : '—';
  const compact = value => valid(value) ? new Intl.NumberFormat('en-US', {notation: 'compact', maximumFractionDigits: 2}).format(value) : '—';
  const price = value => valid(value) ? value.toLocaleString('zh-CN', {maximumFractionDigits: 8}) : '—';
  const ratio = value => valid(value) ? value.toFixed(4) : '—';
  const percent = value => valid(value) ? (value * 100).toFixed(4) + '%' : '—';
  const when = value => value && !String(value).startsWith('0001-') ? new Date(value).toLocaleString('zh-CN', {timeZone: 'Asia/Shanghai', hour12: false}) : '—';
  const tone = value => !valid(value) ? '' : value > 0 ? 'buy' : value < 0 ? 'sell' : '';
  const stateLabel = value => ({ok:'正常',partial:'部分数据缺失',stale:'数据已过期',unavailable:'等待采集'})[value] || value;
  let busy = false, marketRange = '1h', latest = null, liqCursor = '', liqStack = [], liqNext = '', liqAnchor = '';
  let liqSymbol = 'ALL', liqSymbols = [], symbolActive = -1, pendingLoad = false;
  let historyCursor = '', historyRows = [], historyNext = '', historyAnchor = '', frozenBook = null, historyKind = 'events';
  $(page + '-page').hidden = false;
  $('page-subtitle').textContent = ({liquidations:'全部 U 本位合约 · 按时间查看真实采集记录', 'hedge-wall':'盘口挂单墙 · 实时分布与历史记录', 'market-info':'永续市场数据与期权 Gamma/GEX'})[page];
  if (page === 'liquidations') $('symbol').hidden = true;
  async function get(path, params = {}) {
    const response = await fetch(base + 'api/v1/' + path + '?' + new URLSearchParams(params), {cache:'no-store', credentials:'same-origin'});
    if (response.status === 401) { location.assign(base); throw new Error('会话已过期'); }
    const data = await response.json();
    if (!response.ok) throw new Error(data.detail || 'HTTP ' + response.status);
    return data;
  }
  function metric(label, value, detail = '', color = '', cardClass = '') {
    return `<div class="info-metric${cardClass ? ' ' + escape(cardClass) : ''}"><div class="label">${escape(label)}</div><div class="number ${color}">${escape(value)}</div><div class="detail">${escape(detail)}</div></div>`;
  }
  function dateInput(value) { return value ? new Date(value + ':00+08:00').toISOString() : ''; }
  function paramsDates(from, to) {
    const out = {};
    if ($(from).value) out.from = dateInput($(from).value);
    if ($(to).value) out.to = dateInput($(to).value);
    return out;
  }
  function blankRow(columns, text = '暂无记录') { return `<tr><td colspan="${columns}">${escape(text)}</td></tr>`; }
  async function load() {
    if (busy) { pendingLoad = true; return; }
    busy = true; $('page-error').hidden = true;
    try {
      if (page === 'liquidations') {
        if (!liqCursor) liqAnchor = new Date().toISOString();
        const params = {symbol:liqSymbol, side:$('liq-side').value, field:$('liq-field').value, minimum:$('liq-min').value || '0', limit:50, cursor:liqCursor, to:liqAnchor};
        const packet = await get('liquidations', params);
        if (!pendingLoad) { latest = packet; renderLiquidations(packet); }
      } else if (page === 'hedge-wall') {
        latest = await get('hedge-wall', {symbol:$('symbol').value, half_life:$('wall-half').value, window:$('wall-window').value}); renderWalls(latest);
      } else {
        latest = await get('market-info', {symbol:$('symbol').value, range:marketRange}); renderMarket(latest);
      }
    } catch (error) { $('page-error').textContent = error.message; $('page-error').hidden = false; $('status').textContent = '更新失败 · 保留上次数据'; }
    finally { busy = false; if (pendingLoad) { pendingLoad = false; load(); } }
  }
  function resetLiquidations() { liqCursor = ''; liqStack = []; load(); }
  function renderLiquidations(packet) {
    const d = packet.data;
    liqSymbols = [...new Set([...(d.symbols || []), ...(liqSymbol !== 'ALL' ? [liqSymbol] : [])])].sort();
    if (!$('liq-symbol-options').hidden) renderSymbolOptions(false);
    $('liq-coverage').textContent = d.coverage + '。最早记录：' + when(d.available_from);
    const health = packet.sources.binance_liquidations;
    $('liq-source').textContent = health ? '币安强平连接：' + (health.connected ? '已连接' : '断开') + ' · 最近事件 ' + when(health.last_message) + (health.last_error ? ' · ' + health.last_error : '') : '等待连接状态';
    $('liq-periods').innerHTML = d.periods.map(p => metric(p.label + ' 清算金额 USD', compact(p.long_usd + p.short_usd), '多头 ' + compact(p.long_usd) + ' / 空头 ' + compact(p.short_usd) + ' · ' + p.count + ' 笔', '', p.long_usd > p.short_usd ? 'liq-long-dominant' : p.short_usd > p.long_usd ? 'liq-short-dominant' : '')).join('');
    $('liq-rows').innerHTML = d.rows.map(r => `<tr><td>${when(r.event_time)}</td><td>${escape(r.symbol)}</td><td class="${r.position_side === 'long' ? 'sell' : 'buy'}">${r.position_side === 'long' ? '多头被清算' : '空头被清算'}</td><td>${price(r.price)}</td><td>${price(r.quantity)}</td><td>${amount(r.notional_usd)}</td></tr>`).join('') || blankRow(6);
    liqNext = d.next_cursor || ''; $('liq-prev').disabled = liqStack.length === 0; $('liq-next').disabled = !liqNext;
    $('liq-page').textContent = `第 ${liqStack.length + 1} 页 · 每页 50 条 · ${liqCursor ? '历史页保持稳定，返回第一页查看新事件' : '每 5 秒更新'}`;
    $('status').textContent = '更新于 ' + when(new Date()) + (liqCursor ? ' · 历史分页' : ' · 实时');
  }
  function symbolLabel(value) { return value === 'ALL' ? '全部币对' : value; }
  function closeSymbolOptions(restore = true) {
    $('liq-symbol-options').hidden = true;
    $('liq-symbol-search').setAttribute('aria-expanded', 'false');
    $('liq-symbol-search').removeAttribute('aria-activedescendant');
    if (restore) $('liq-symbol-search').value = symbolLabel(liqSymbol);
    symbolActive = -1;
  }
  function renderSymbolOptions(reset = true) {
    const input = $('liq-symbol-search'), list = $('liq-symbol-options');
    const activeValue = reset ? null : list.querySelector('[data-active="true"]')?.dataset.symbol;
    const query = input.value === symbolLabel(liqSymbol) ? '' : input.value.trim().toUpperCase();
    const values = ['ALL', ...liqSymbols].filter(value => !query || symbolLabel(value).toUpperCase().includes(query) || value.includes(query));
    list.innerHTML = values.map((value, i) => `<li id="liq-symbol-option-${i}" role="option" aria-selected="${value === liqSymbol}" data-symbol="${escape(value)}">${escape(symbolLabel(value))}</li>`).join('') || '<li class="muted" role="presentation">无匹配币对</li>';
    symbolActive = activeValue ? values.indexOf(activeValue) : -1;
    list.hidden = false; input.setAttribute('aria-expanded', 'true');
    highlightSymbolOption();
  }
  function highlightSymbolOption() {
    const options = [...$('liq-symbol-options').querySelectorAll('[role="option"]')];
    options.forEach((option, i) => { option.dataset.active = String(i === symbolActive); });
    const active = options[symbolActive];
    if (active) { $('liq-symbol-search').setAttribute('aria-activedescendant', active.id); active.scrollIntoView({block:'nearest'}); }
    else $('liq-symbol-search').removeAttribute('aria-activedescendant');
  }
  function chooseSymbol(value) {
    liqSymbol = value; closeSymbolOptions(); resetLiquidations();
  }
  function wallRows(events, side) {
    const labels = {disappeared:'已消失',disconnected:'连接中断',restart:'服务重启'};
    return events.filter(e => e.side === side).map(e => `<tr><td>${price(e.price)}</td><td>${compact(e.peak_usd)}</td><td>${(e.duration_ms / 1000).toFixed(1)} 秒</td><td>${when(e.started_at)}</td><td>${e.ended_at ? escape(labels[e.end_reason] || e.end_reason) : '<span class="buy">持续中</span>'}</td></tr>`).join('') || blankRow(5);
  }
  function renderWalls(d) {
    const b = frozenBook || d.book;
    $('status').textContent = frozenBook ? '历史快照 · ' + when(frozenBook.time) : stateLabel(d.state) + ' · ' + when(b?.time);
    $('wall-metrics').innerHTML = metric('Best Bid', price(b?.best_bid), '买盘最佳报价', 'buy') + metric('Best Ask', price(b?.best_ask), '卖盘最佳报价', 'sell') + metric('盘口价差', b ? price(b.best_ask - b.best_bid) : '—', '原始价格单位') + metric('快照时间', b ? when(b.time) : '—', '每 5 秒入库 · 历史保留 30 天');
    $('bid-events').innerHTML = wallRows(d.events, 'bid'); $('ask-events').innerHTML = wallRows(d.events, 'ask');
    drawDepth(b, frozenBook ? {} : d);
    $('wall-detail').textContent = '桶宽 ' + price(b?.bucket_width) + ' · 25 万 USD / 第 85 百分位阈值 · 相邻墙确认 · 持续至少 3 秒 · 悬停查看价格与金额';
  }
  async function loadHistory(reset) {
    if (reset) { historyCursor = ''; historyRows = []; historyAnchor = new Date().toISOString(); historyKind = $('history-kind').value; }
    try {
      const d = await get('hedge-wall/history', {symbol:$('symbol').value, kind:historyKind, limit:50, cursor:historyCursor, to:historyAnchor, ...paramsDates('history-from','history-to')});
      historyRows = historyRows.concat(historyKind === 'events' ? d.events : d.snapshots); historyNext = d.next_cursor || ''; $('history-more').disabled = !historyNext;
      if (historyKind === 'events') {
        $('history-results').innerHTML = '<table><thead><tr><th>出现时间</th><th>方向</th><th>价格</th><th>峰值 USD</th><th>持续秒数</th><th>结束时间</th><th>结束原因</th></tr></thead><tbody>' + historyRows.map(e => `<tr><td>${when(e.started_at)}</td><td class="${e.side === 'bid' ? 'buy' : 'sell'}">${e.side === 'bid' ? '买盘' : '卖盘'}</td><td>${price(e.price)}</td><td>${compact(e.peak_usd)}</td><td>${(e.duration_ms/1000).toFixed(1)}</td><td>${when(e.ended_at)}</td><td>${escape(({disappeared:'墙消失',disconnected:'连接中断',restart:'服务重启'})[e.end_reason] || '持续中')}</td></tr>`).join('') + (historyRows.length ? '' : blankRow(7)) + '</tbody></table>';
      } else {
        $('history-results').innerHTML = '<table><thead><tr><th>时间</th><th>Best Bid</th><th>Best Ask</th><th>买/卖档数</th><th>查看</th></tr></thead><tbody>' + historyRows.map((b,i) => `<tr><td>${when(b.time)}</td><td>${price(b.best_bid)}</td><td>${price(b.best_ask)}</td><td>${b.bids.length} / ${b.asks.length}</td><td><button class="history-select" type="button" data-snapshot="${i}">查看图表</button></td></tr>`).join('') + (historyRows.length ? '' : blankRow(5)) + '</tbody></table>';
      }
    } catch (error) { $('page-error').textContent = error.message; $('page-error').hidden = false; }
  }
  function env(id) {
    const canvas = $(id), rect = canvas.getBoundingClientRect(), width = Math.max(280, rect.width), height = Math.max(180, rect.height), dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(width * dpr); canvas.height = Math.round(height * dpr);
    const ctx = canvas.getContext('2d'); ctx.setTransform(dpr,0,0,dpr,0,0); ctx.fillStyle = paint('#07141d'); ctx.fillRect(0,0,width,height); ctx.font = '11px system-ui';
    return {canvas,ctx,width,height};
  }
  function emptyChart(id, text = '等待数据') { const {ctx} = env(id); ctx.fillStyle = paint('#83a0b2'); ctx.fillText(text,20,32); }
  function lineChart(id, points, lines) {
    if (!points.length) { emptyChart(id); return; }
    const {canvas,ctx,width,height} = env(id), pad = {l:65,r:16,t:18,b:36};
    const values = points.flatMap(p => lines.map(l => p[l.key])).filter(valid);
    if (!values.length) { emptyChart(id,'该窗口暂无完整数据'); return; }
    let low = Math.min(...values), high = Math.max(...values); const span = Math.max(Math.abs(high)*.02, high-low, 1e-6); low -= span*.08; high += span*.08;
    const x = i => pad.l + i / Math.max(1,points.length-1) * (width-pad.l-pad.r), y = v => pad.t + (high-v)/(high-low)*(height-pad.t-pad.b);
    ctx.strokeStyle = paint('#213946'); ctx.fillStyle = paint('#83a0b2');
    for(let i=0;i<5;i++){const v=low+(high-low)*i/4,py=y(v);ctx.beginPath();ctx.moveTo(pad.l,py);ctx.lineTo(width-pad.r,py);ctx.stroke();ctx.fillText(compact(v),4,py+4);}
    for(let i=0;i<3;i++){const index=Math.round((points.length-1)*i/2),label=new Date(points[index].time).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}),labelWidth=ctx.measureText(label).width;ctx.fillText(label,Math.max(pad.l,Math.min(x(index)-labelWidth/2,width-pad.r-labelWidth)),height-10);}
    lines.forEach((l,j)=>{ctx.strokeStyle=l.color;ctx.lineWidth=1.8;ctx.beginPath();let drawing=false;points.forEach((p,i)=>{const v=p[l.key];if(!valid(v)){drawing=false;return;}if(drawing)ctx.lineTo(x(i),y(v));else{ctx.moveTo(x(i),y(v));drawing=true;}if(points.length===1){ctx.lineTo(x(i)+3,y(v));}});ctx.stroke();ctx.fillStyle=l.color;ctx.fillText(l.label,pad.l+j*140,14);});
    canvas.onpointermove = event => {const i=Math.max(0,Math.min(points.length-1,Math.round((event.offsetX-pad.l)/(width-pad.l-pad.r)*(points.length-1))));canvas.title=when(points[i].time)+'\n'+lines.map(l=>l.label+' '+amount(points[i][l.key])).join('\n');};
  }
  function drawDepth(book, data) {
    if (!book || !book.bids.length || !book.asks.length) { emptyChart('wall-chart','等待盘口同步'); return; }
    const {canvas,ctx,width,height} = env('wall-chart'), pad={l:65,r:20,t:20,b:38};
    const raw=[...book.bids,...book.asks], min=Math.min(...raw.map(l=>l.price)),max=Math.max(...raw.map(l=>l.price)),span=Math.max(max-min,book.bucket_width*2),low=min-span*.04,high=max+span*.04;
    const x = p=>pad.l+(p-low)/(high-low)*(width-pad.l-pad.r), inside=lv=>lv.price>=low&&lv.price<=high;
    const series=[{rows:data.ghost_bids||[],color:paint('#2dd4bf'),alpha:.18},{rows:data.ghost_asks||[],color:paint('#fb7185'),alpha:.18},{rows:book.bids,color:paint('#2dd4bf'),alpha:1},{rows:book.asks,color:paint('#fb7185'),alpha:1}];
    const maximum=Math.max(1,...series.flatMap(s=>s.rows.filter(inside).map(l=>l.notional_usd)));
    const y = n=>height-pad.b-n/maximum*(height-pad.t-pad.b),bar=Math.max(1,Math.min(16,book.bucket_width/(high-low)*(width-pad.l-pad.r)*.8));
    ctx.fillStyle=paint('#83a0b2');ctx.strokeStyle=paint('#213946');for(let i=0;i<5;i++){const n=maximum*i/4,py=y(n);ctx.beginPath();ctx.moveTo(pad.l,py);ctx.lineTo(width-pad.r,py);ctx.stroke();ctx.fillText(compact(n),4,py+4);}
    for(const s of series){ctx.fillStyle=s.color;ctx.globalAlpha=s.alpha;for(const lv of s.rows.filter(inside))ctx.fillRect(x(lv.price)-bar/2,y(lv.notional_usd),bar,height-pad.b-y(lv.notional_usd));}ctx.globalAlpha=1;
    const mid=(book.best_bid+book.best_ask)/2;ctx.strokeStyle=paint('#fbbf24');ctx.setLineDash([4,4]);ctx.beginPath();ctx.moveTo(x(mid),pad.t);ctx.lineTo(x(mid),height-pad.b);ctx.stroke();ctx.setLineDash([]);ctx.fillStyle=paint('#83a0b2');const tickCount=width<500?3:5;for(let i=0;i<tickCount;i++){const p=low+(high-low)*i/(tickCount-1),label=price(Math.round(p/book.bucket_width)*book.bucket_width),labelWidth=ctx.measureText(label).width;ctx.fillText(label,Math.max(pad.l,Math.min(x(p)-labelWidth/2,width-pad.r-labelWidth)),height-12);}
    canvas.onpointermove=event=>{const p=low+(event.offsetX-pad.l)/(width-pad.l-pad.r)*(high-low),lv=raw.reduce((best,l)=>Math.abs(l.price-p)<Math.abs(best.price-p)?l:best,raw[0]);canvas.title='价格 '+price(lv.price)+'\n挂单金额 USD '+amount(lv.notional_usd)+'\n数量 '+price(lv.quantity);};
  }
  function gammaChart(g) {
    if (!g.levels?.length) { emptyChart('gamma-chart','暂无可用期权链'); return; }
    const {canvas,ctx,width,height}=env('gamma-chart'),levels=g.levels,maximum=Math.max(1,...levels.map(l=>Math.abs(l.net_gex_usd))),left=65,right=20,top=20,bottom=35,middle=(height-bottom+top)/2,step=(width-left-right)/levels.length;
    ctx.strokeStyle=paint('#213946');ctx.beginPath();ctx.moveTo(left,middle);ctx.lineTo(width-right,middle);ctx.stroke();
    for(let i=0;i<levels.length;i++){const l=levels[i],h=l.net_gex_usd/maximum*(height-top-bottom)/2;ctx.fillStyle=l.net_gex_usd>=0?paint('#2dd4bf'):paint('#fb7185');ctx.fillRect(left+i*step,Math.min(middle,middle-h),Math.max(1,step*.75),Math.abs(h));}
    ctx.fillStyle=paint('#83a0b2');ctx.fillText(compact(maximum),4,top+8);ctx.fillText(compact(-maximum),4,height-bottom);for(let i=0;i<3;i++){const index=Math.round((levels.length-1)*i/2);ctx.fillText(price(levels[index].strike),Math.max(left,Math.min(left+index*step,width-95)),height-10);}
    canvas.onpointermove=event=>{const i=Math.max(0,Math.min(levels.length-1,Math.floor((event.offsetX-left)/step))),l=levels[i];canvas.title='行权价 '+price(l.strike)+'\n净 GEX USD '+amount(l.net_gex_usd)+'\n绝对 GEX USD '+amount(l.absolute_gex_usd);};
  }
  function renderMarket(d) {
    const c=d.current||{},g=d.gamma||{},last=d.series[d.series.length-1]||{};
    $('status').textContent=stateLabel(d.state)+' · 更新 '+when(c.time);
    $('market-metrics').innerHTML=metric('标记价格',price(c.mark_price),'24h '+percent(c.change_24h),tone(c.change_24h))+metric('24h 成交额 USD',compact(c.volume_24h_usd),'低 '+price(c.low_24h)+' / 高 '+price(c.high_24h))+metric('OI 价值 USD',compact(c.oi_usd),'数量 '+price(c.oi_quantity))+metric('资金费率',percent(c.funding_rate),'下次 '+when(c.next_funding))+metric('盘口价差',valid(c.best_ask)&&valid(c.best_bid)?price(c.best_ask-c.best_bid):'—','Bid '+price(c.best_bid)+' / Ask '+price(c.best_ask))+metric('全账户多空比',ratio(c.account_ratio),'账户数量比例')+metric('顶级持仓多空比',ratio(c.top_position_ratio),'顶级交易员持仓比例')+metric('净仓估算 USD',compact(c.net_position_usd),'基于 OI 与顶级持仓比',tone(c.net_position_usd))+metric('最新采样主动买 USD',compact(last.buy_usd),'所选范围最后一个完整采样', 'buy')+metric('最新采样主动卖 USD',compact(last.sell_usd),'所选范围最后一个完整采样', 'sell');
    lineChart('oi-chart',d.series,[{key:'oi_usd',label:'OI USD',color:paint('#60a5fa')}]);lineChart('cvd-chart',d.series,[{key:'cvd_usd',label:'CVD USD',color:paint('#fbbf24')}]);lineChart('taker-chart',d.series,[{key:'buy_usd',label:'主动买 USD',color:paint('#2dd4bf')},{key:'sell_usd',label:'主动卖 USD',color:paint('#fb7185')}]);lineChart('ratio-chart',d.series,[{key:'account_ratio',label:'全账户',color:paint('#60a5fa')},{key:'top_position_ratio',label:'顶级持仓',color:paint('#c084fc')}]);lineChart('delta-chart',d.series,[{key:'delta_usd',label:'主动买卖差值 USD',color:paint('#2dd4bf')}]);
    $('market-windows').innerHTML=d.windows.map(w=>`<tr><td>${escape(w.label)}</td><td class="${tone(w.oi_delta_usd)}">${compact(w.oi_delta_usd)}</td><td class="${tone(w.net_position_delta_usd)}">${compact(w.net_position_delta_usd)}</td><td class="${tone(w.cvd_delta_usd)}">${compact(w.cvd_delta_usd)}</td><td>${escape(w.analysis)}</td></tr>`).join('');
    $('gamma-status').textContent=stateLabel(g.state)+' · '+when(g.time);$('gamma-method').textContent=g.method||'正在读取期权链；缺失数据不记为零。';
    const usable=['ok','partial','stale'].includes(g.state);
    $('gamma-metrics').innerHTML=metric('净 GEX USD',usable?compact(g.net_gex_usd):'—','1% 标的价格变化',tone(g.net_gex_usd))+metric('绝对 GEX USD',usable?compact(g.absolute_gex_usd):'—','逐合约绝对敞口之和')+metric('Gamma Wall',price(g.gamma_wall),'绝对 GEX 最大的行权价')+metric('合约覆盖',usable?g.contracts+' / '+g.expected_contracts:'—',(g.expiries?.length||0)+' 个到期日');
    gammaChart(g);$('gamma-expiries').innerHTML=(g.expiries||[]).map(e=>`<tr><td>${escape(e.expiry)}</td><td>${e.contracts}</td><td class="${tone(e.net_gex_usd)}">${amount(e.net_gex_usd)}</td></tr>`).join('')||blankRow(3);
    $('market-warnings').textContent=[...(c.warnings||[]),...(g.warnings||[]),...(d.state==='stale'?['永续指标已过期']:[]),...(g.state==='stale'?['期权数据已过期']:[])].join('；');
  }
  $('refresh').addEventListener('click',load);
  $('symbol').addEventListener('change',()=>{liqCursor='';liqStack=[];frozenBook=null;historyRows=[];$('history-results').innerHTML='';$('history-more').disabled=true;load();});
  if(page==='liquidations'){
    const input = $('liq-symbol-search'), list = $('liq-symbol-options');
    input.addEventListener('focus', () => { input.select(); renderSymbolOptions(); });
    input.addEventListener('click', () => { if (list.hidden) renderSymbolOptions(); });
    input.addEventListener('input', () => renderSymbolOptions());
    input.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); closeSymbolOptions(); return; }
      if (event.key === 'Tab') { closeSymbolOptions(); return; }
      if (!['ArrowDown','ArrowUp','Enter'].includes(event.key)) return;
      if (event.key === 'Enter' && list.hidden) return;
      event.preventDefault();
      if (list.hidden) renderSymbolOptions();
      const options = [...list.querySelectorAll('[role="option"]')];
      if (!options.length) return;
      if (event.key === 'Enter') { chooseSymbol(options[Math.max(0, symbolActive)].dataset.symbol); return; }
      symbolActive = event.key === 'ArrowDown' ? (symbolActive + 1) % options.length : symbolActive < 0 ? options.length - 1 : (symbolActive - 1 + options.length) % options.length;
      highlightSymbolOption();
    });
    $('liq-symbol-toggle').addEventListener('click', () => {
      if (list.hidden) { input.focus(); renderSymbolOptions(); } else closeSymbolOptions();
    });
    list.addEventListener('pointerdown', event => { if (event.target.closest('[data-symbol]')) event.preventDefault(); });
    list.addEventListener('click', event => { const option = event.target.closest('[data-symbol]'); if (option) chooseSymbol(option.dataset.symbol); });
    $('liq-symbol-all').addEventListener('click', () => chooseSymbol('ALL'));
    document.addEventListener('click', event => { if (!event.target.closest('#liq-symbol-picker')) closeSymbolOptions(); });
    $('liquidation-filters').addEventListener('submit',event=>{event.preventDefault();resetLiquidations();});
    $('liq-next').addEventListener('click',()=>{if(busy||!liqNext)return;liqStack.push(liqCursor);liqCursor=liqNext;load();});
    $('liq-prev').addEventListener('click',()=>{if(busy||!liqStack.length)return;liqCursor=liqStack.pop();load();});
  } else if(page==='hedge-wall'){
    for(const id of ['wall-half','wall-window'])$(id).addEventListener('change',load);
    $('wall-history-filters').addEventListener('submit',event=>{event.preventDefault();loadHistory(true);});
    $('history-more').addEventListener('click',()=>{if(!historyNext)return;historyCursor=historyNext;loadHistory(false);});
    $('history-results').addEventListener('click',event=>{const button=event.target.closest('[data-snapshot]');if(button){frozenBook=historyRows[Number(button.dataset.snapshot)];if(latest)renderWalls(latest);$('wall-chart').scrollIntoView({behavior:'smooth',block:'center'});}});
    $('history-live').addEventListener('click',()=>{frozenBook=null;if(latest)renderWalls(latest);});
  } else {
    const ranges=['5m','15m','1h','4h','8h','12h','24h','2d','3d','7d'];$('market-ranges').innerHTML=ranges.map(r=>`<button type="button" data-range="${r}" class="${r===marketRange?'selected':''}">${r.toUpperCase()}</button>`).join('');
    $('market-ranges').addEventListener('click',event=>{const b=event.target.closest('[data-range]');if(!b||busy)return;marketRange=b.dataset.range;for(const button of $('market-ranges').children)button.classList.toggle('selected',button===b);load();});
  }
  function repaint() { if(!latest)return;if(page==='hedge-wall')renderWalls(latest);if(page==='market-info')renderMarket(latest); }
  window.addEventListener('resize',repaint);
  window.addEventListener('themechange',repaint);
  load();setInterval(()=>{if(!document.hidden)load();},page==='market-info'?60000:5000);
})();
