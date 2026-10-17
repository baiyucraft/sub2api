async (page) => {
  const fixture=page.__upstreamFixture;
  const session=await page.context().newCDPSession(page);
  try {
  await session.send('Performance.enable');
  const recordTrace=page.__recordTrace===true;
  const traceEvents=[];
  if(recordTrace) {
    session.on('Tracing.dataCollected',event=>traceEvents.push(...event.value));
    await session.send('Tracing.start',{categories:'devtools.timeline,blink.user_timing,v8',options:'record-as-much-as-possible'});
  }
  const refresh=page.locator('main button.btn-secondary').first();
  if(!await page.getByRole('button',{name:'Select option',exact:true}).getByText('100',{exact:true}).count()) {
    await page.getByRole('button',{name:'Select option',exact:true}).click();
    await page.getByRole('option',{name:'100',exact:true}).click();
  }
  await page.getByText(/^fixture-account-001/).first().waitFor();
  await page.locator('[data-upstream-health-state]').first().scrollIntoViewIfNeeded();
  await new Promise(resolve=>setTimeout(resolve,350));
  await page.evaluate(()=>{
    window.__refreshLongTasks=[];
    window.__refreshObserver=new PerformanceObserver(list=>{for(const entry of list.getEntries())window.__refreshLongTasks.push({start:entry.startTime,duration:entry.duration,scenario:window.__refreshScenario});});
    window.__refreshObserver.observe({type:'longtask',buffered:false});
  });
  const percentile=(values,p)=>[...values].sort((a,b)=>a-b)[Math.max(0,Math.ceil(values.length*p)-1)]||0;
  const report={api:'mock fixture',build:'production dist',pageSize:100,viewport:{width:1440,height:1000},recordTrace,scenarios:[],errors:fixture.errors};
  let responseMetrics;
  fixture.beforeFulfill=async()=>{responseMetrics=Object.fromEntries((await session.send('Performance.getMetrics')).metrics.map(metric=>[metric.name,metric.value]));};
  for(const rate of [1,4]){
    await session.send('Emulation.setCPUThrottlingRate',{rate});
    for(const mode of ['unchanged','single','full']){
      await page.evaluate(scenario=>{window.__refreshScenario=scenario;},`${rate}x-${mode}`);
      const samples=[];
      for(let index=0;index<30;index++){
        if(mode==='single') fixture.accounts[0].name=`fixture-account-001-sample-${index}`;
        if(mode==='full') {
          for(const account of fixture.accounts)account.upstream_health={...account.upstream_health,status:index%2?'healthy':'degraded',consecutive_failures:index%2?0:1,reason:index%2?'traffic_success':'traffic_error'};
          fixture.quality={...fixture.quality,recent_1h:{...fixture.quality.recent_1h,quality_score:90+index%2}};
        }
        fixture.version++;
        const started=Date.now();
        const response=page.waitForResponse(r=>r.url().includes('/upstream-management/accounts'));
        await refresh.click(); await response;
        await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
        const after=Object.fromEntries((await session.send('Performance.getMetrics')).metrics.map(metric=>[metric.name,metric.value]));
        samples.push({elapsedMs:Date.now()-started,taskMs:(after.TaskDuration-responseMetrics.TaskDuration)*1000,scriptMs:(after.ScriptDuration-responseMetrics.ScriptDuration)*1000,layoutMs:(after.LayoutDuration-responseMetrics.LayoutDuration)*1000});
      }
      report.scenarios.push({rate,mode,samples,taskP95:percentile(samples.map(s=>s.taskMs),.95),interactionP95:percentile(samples.map(s=>s.elapsedMs),.95),layoutP95:percentile(samples.map(s=>s.layoutMs),.95)});
    }
  }
  await session.send('Emulation.setCPUThrottlingRate',{rate:1});
  fixture.beforeFulfill=null;
  report.longTasks=await page.evaluate(()=>{window.__refreshObserver.disconnect();return window.__refreshLongTasks;});
  report.dom=await session.send('Memory.getDOMCounters');
  report.heap=await session.send('Runtime.getHeapUsage');
  if(recordTrace) {
    const stopped=new Promise(resolve=>session.once('Tracing.tracingComplete',resolve));
    await session.send('Tracing.end'); await stopped;
    const downloadPromise=page.waitForEvent('download');
    await page.evaluate(json=>{const anchor=document.createElement('a');anchor.href=URL.createObjectURL(new Blob([json],{type:'application/json'}));anchor.download='cpu-trace.json';anchor.click();setTimeout(()=>URL.revokeObjectURL(anchor.href),1000);},JSON.stringify({traceEvents}));
    const download=await downloadPromise;
    await download.saveAs('.tmp/upstream-table-browser-cpu-trace.json');
  }
  return report;
  } finally {
    fixture.beforeFulfill=null;
    await page.evaluate(()=>window.__refreshObserver?.disconnect()).catch(()=>{});
    await session.send('Emulation.setCPUThrottlingRate',{rate:1}).catch(()=>{});
    await session.send('Tracing.end').catch(()=>{});
    await session.detach().catch(()=>{});
  }
}
