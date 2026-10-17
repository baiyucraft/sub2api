async (page) => {
  if(page.__stabilityTimer) clearInterval(page.__stabilityTimer);
  if(!await page.getByRole('button',{name:'Select option',exact:true}).getByText('100',{exact:true}).count()) {
    await page.getByRole('button',{name:'Select option',exact:true}).click();
    await page.getByRole('option',{name:'100',exact:true}).click();
  }
  await page.getByText(/^fixture-account-001/).first().waitFor();
  await page.evaluate(()=>{delete window.__initialRows;});
  const session=await page.context().newCDPSession(page);
  await session.send('HeapProfiler.collectGarbage');
  page.__stabilitySession=session;
  const start=Date.now();
  const state={startedAt:new Date(start).toISOString(),targetDurationMs:120000,pageSize:100,api:'mock fixture',build:'local production dist',samples:[],finished:false};
  page.__stability=state;
  const sample=async()=>{
    const dom=await session.send('Memory.getDOMCounters');
    const heap=await session.send('Runtime.getHeapUsage');
    const fixture=page.__upstreamFixture;
    state.samples.push({elapsedMs:Date.now()-start,...dom,...heap,listRequests:fixture.requests.filter(r=>r.path.endsWith('/upstream-management/accounts')).length,qualityRequests:fixture.requests.filter(r=>r.path.includes('quality-stats')).length,errors:[...fixture.errors],visible:await page.evaluate(()=>document.visibilityState)});
  };
  await sample();
  page.__stabilityTimer=setInterval(async()=>{
    try{
      await sample();
      if(Date.now()-start>=state.targetDurationMs){
        clearInterval(page.__stabilityTimer);
        await session.send('HeapProfiler.collectGarbage');
        state.postGCHeap=await session.send('Runtime.getHeapUsage');
        state.postGCDOM=await session.send('Memory.getDOMCounters');
        state.finished=true;state.finishedAt=new Date().toISOString();
        await session.detach();
      }
    }catch(error){state.sampleError=String(error);clearInterval(page.__stabilityTimer);await session.detach().catch(()=>{});}
  },10000);
  return state;
}
