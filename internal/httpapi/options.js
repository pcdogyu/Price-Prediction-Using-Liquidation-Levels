'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const base = document.querySelector('meta[name="app-base"]').content;
  const valid = value => typeof value === 'number' && Number.isFinite(value);
  const when = value => value ? new Date(value).toLocaleString('zh-CN', {timeZone:'Asia/Shanghai', hour12:false}) : '—';
  const gamma = value => valid(value) ? value.toFixed(6) : '—';
  const escape = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
  const labels = {ok:'正常', partial:'部分数据缺失', stale:'数据已过期', unavailable:'等待采集'};
  const color = token => getComputedStyle(document.documentElement).getPropertyValue('--' + token).trim();
  let latest = null, controller, sequence = 0;
  let hoverPoints = [], hoverWidth = 0, hoverX = null;

  function draw(data) {
    const canvas = $('options-chart'), bounds = canvas.getBoundingClientRect(), dpr = devicePixelRatio || 1;
    const width = Math.max(1, bounds.width), height = Math.max(1, bounds.height);
    canvas.width = Math.round(width*dpr); canvas.height = Math.round(height*dpr);
    const ctx = canvas.getContext('2d'); ctx.scale(dpr,dpr); ctx.font = '11px system-ui';
    hoverPoints = []; hoverWidth = width; hoverX = null; $('options-tooltip').hidden = true;
    const points = data.series.flatMap(series => series.points).filter(point => valid(point.gamma) && Number.isFinite(Date.parse(point.time)));
    const values = points.map(point => Math.abs(point.gamma));
    const maximum = Math.max(.05, ...values)*1.12;
    const plot = {left:width<500?48:66, right:width-18, top:28, bottom:height-40};
    // On a new installation show the actual collected span, without fabricating earlier history.
    const start = points.length ? Math.max(Date.parse(data.from), Math.min(...points.map(point => Date.parse(point.time)))-30000) : Date.parse(data.from);
    const end = Math.max(start+60000, Date.parse(data.to));
    const x = at => plot.left+(at-start)/(end-start)*(plot.right-plot.left);
    const y = value => plot.top+(maximum-value)/(2*maximum)*(plot.bottom-plot.top);
    ctx.strokeStyle=color('chart-grid'); ctx.fillStyle=color('muted');
    for (let i=0;i<5;i++) {
      const value=maximum-i*maximum/2, yy=y(value);
      ctx.beginPath(); ctx.moveTo(plot.left,yy); ctx.lineTo(plot.right,yy); ctx.stroke();
      ctx.fillText(value.toFixed(4),3,yy+4);
    }
    const ticks=width<500?3:7;
    for (let i=0;i<ticks;i++) {
      const at=start+(end-start)*i/(ticks-1), xx=x(at);
      ctx.strokeStyle=color('chart-grid'); ctx.beginPath(); ctx.moveTo(xx,plot.top); ctx.lineTo(xx,plot.bottom); ctx.stroke();
      const text=new Date(at).toLocaleTimeString('zh-CN',{timeZone:'Asia/Shanghai',hour:'2-digit',minute:'2-digit',hour12:false});
      ctx.fillText(text,Math.max(plot.left,Math.min(plot.right-ctx.measureText(text).width,xx-ctx.measureText(text).width/2)),height-12);
    }
    ctx.strokeStyle=color('options-zero'); ctx.lineWidth=1.5; ctx.beginPath(); ctx.moveTo(plot.left,y(0)); ctx.lineTo(plot.right,y(0)); ctx.stroke();
    ctx.fillStyle=color('options-zero'); ctx.fillText('0 轴',plot.right-30,y(0)-7);
    ctx.save(); ctx.beginPath(); ctx.rect(plot.left,plot.top,plot.right-plot.left,plot.bottom-plot.top); ctx.clip();
    for (const series of data.series) {
      const lineColor=color(series.underlying==='BTC'?'options-btc':'options-eth');
      ctx.strokeStyle=lineColor; ctx.fillStyle=lineColor; ctx.lineWidth=2;
      let previous=null;
      ctx.beginPath();
      for (const point of series.points) {
        const at=Date.parse(point.time);
        if (!valid(point.gamma) || !Number.isFinite(at)) {previous=null;continue;}
        const px=x(at),py=y(point.gamma);
        if (previous && at-previous.time<=data.refresh_seconds*2500) ctx.lineTo(px,py); else ctx.moveTo(px,py);
        previous={time:at};
        hoverPoints.push({x:px,y:py,point,series});
      }
      ctx.stroke();
      for (const point of hoverPoints.filter(point=>point.series===series)) {ctx.beginPath();ctx.arc(point.x,point.y,2.3,0,Math.PI*2);ctx.fill();}
    }
    ctx.restore();
    if (!points.length) {ctx.fillStyle=color('muted');ctx.fillText('等待 Deribit Gamma 采集 · 历史从本服务采集起累计',plot.left+8,plot.top+25);}
    canvas.dataset.points=points.length; canvas.dataset.rangeStart=new Date(start).toISOString(); canvas.dataset.rangeEnd=new Date(end).toISOString();
  }

  function render(packet) {
    const data=packet.data;
    $('options-method').textContent=data.method;
    $('options-status').textContent='已加载 '+data.series.reduce((sum,series)=>sum+series.points.length,0)+' 个点 · 每分钟更新';
    $('options-window').textContent='窗口 '+data.hours+' 小时 · 查询截止 '+when(data.to)+' · 历史起始 '+when(data.available_from);
    $('options-rows').innerHTML=data.series.map(series=>{
      const point=series.latest;
      const sourceTime=Date.parse(point?.source_to)>0?point.source_to:point?.time;
      const state=[labels[series.state]||series.state,series.last_error?'本次采集失败 · 保留上次成功数据：'+series.last_error:point?.warning||''].filter(Boolean).join(' · ');
      return '<tr><td>'+escape(series.symbol)+'</td><td>'+escape(series.underlying)+'</td><td class="'+(series.underlying==='BTC'?'options-btc':'options-eth')+'">'+gamma(point?.gamma)+'</td><td>'+escape(point?point.contracts+' / '+point.selected_contracts+'（有持仓 '+point.eligible_contracts+'）':'—')+'</td><td class="'+(series.state==='ok'?'buy':'warning')+'">'+escape(state)+'</td><td title="采集完成 '+escape(when(point?.time))+'">'+when(sourceTime)+'</td></tr>';
    }).join('');
    draw(data);
  }

  async function load() {
    if (!$('options-filters').reportValidity()) return;
    controller?.abort(); controller=new AbortController();
    const current=++sequence;
    const signal=controller.signal;
    $('options-status').textContent='正在读取 Deribit Gamma…';
    try {
      const response=await fetch(base+'api/v1/options?'+new URLSearchParams({hours:$('options-hours').value}),{signal,cache:'no-store',credentials:'same-origin'});
      if(response.status===401){location.assign(base);return;}
      const packet=await response.json();
      if (!response.ok) throw new Error(packet.detail||'HTTP '+response.status);
      if (current!==sequence) return;
      if (!Array.isArray(packet?.data?.series) || packet.data.series.length!==2) throw new Error('期权数据响应无效');
      latest=packet; $('options-error').hidden=true; render(packet);
    } catch(error) {
      if(signal.aborted||current!==sequence) return;
      $('options-error').textContent='期权更新失败：'+error.message;
      $('options-error').hidden=false;
      $('options-status').textContent='更新失败 · '+(latest?'保留上次 '+latest.data.hours+' 小时窗口数据':'等待有效数据');
    }
  }

  $('options-filters').addEventListener('submit',event=>{event.preventDefault();load();});
  $('options-hours').addEventListener('change',load);
  $('options-chart').addEventListener('pointermove',event=>{
    if (!hoverPoints.length) return;
    const rect=event.currentTarget.getBoundingClientRect(),px=(event.clientX-rect.left)*hoverWidth/rect.width;
    hoverX=px;
    const closest=hoverPoints.reduce((best,point)=>Math.abs(point.x-hoverX)<Math.abs(best.x-hoverX)?point:best,hoverPoints[0]);
    const nearby=latest.data.series.map(series=>hoverPoints.filter(point=>point.series===series).reduce((best,point)=>!best||Math.abs(point.x-px)<Math.abs(best.x-px)?point:best,null)).filter(Boolean);
    const tooltip=$('options-tooltip');
    tooltip.textContent=when(closest.point.time)+'\n'+nearby.map(point=>point.series.symbol+' '+gamma(point.point.gamma)+' · '+when(point.point.time)).join('\n');
    tooltip.hidden=false; tooltip.style.left=Math.max(8,Math.min(rect.width-tooltip.offsetWidth-8,px+12))+'px'; tooltip.style.top='12px';
  });
  $('options-chart').addEventListener('pointerleave',()=>{$('options-tooltip').hidden=true;});
  const repaint=()=>{if(latest)draw(latest.data);};
  window.addEventListener('resize',repaint); window.addEventListener('themechange',repaint);
  load(); setInterval(()=>{if(!document.hidden)load();},60000);
})();
