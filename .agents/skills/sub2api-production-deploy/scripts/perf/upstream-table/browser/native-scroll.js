async (page) => {
  if(!await page.getByRole('button',{name:'Select option',exact:true}).getByText('100',{exact:true}).count()) {
    await page.getByRole('button',{name:'Select option',exact:true}).click();
    await page.getByRole('option',{name:'100',exact:true}).click();
  }
  await page.locator('[data-upstream-health-state]').first().scrollIntoViewIfNeeded();
  const session=await page.context().newCDPSession(page);
  try {
  const result={api:'mock fixture',pageSize:100,samples:[]};
  for(const viewport of [{width:1440,height:1000},{width:390,height:844}]) {
    await page.setViewportSize(viewport);
    await new Promise(resolve=>setTimeout(resolve,350));
    for(const rate of [1,4]) {
      await session.send('Emulation.setCPUThrottlingRate',{rate});
      const targetInfo=await page.evaluate(()=>{
        const targets=[...document.querySelectorAll('main div')].filter(node=>node.scrollHeight>node.clientHeight+400 && getComputedStyle(node).overflowY==='auto');
        const target=targets.sort((a,b)=>b.clientHeight-a.clientHeight)[0]||document.scrollingElement;
        target.scrollTop=0;
        const rect=target===document.scrollingElement?{left:0,top:0,width:innerWidth,height:innerHeight}:target.getBoundingClientRect();
        return {target:target.className,scrollHeight:target.scrollHeight,clientHeight:target.clientHeight,cards:document.querySelectorAll('.mobile-content-visibility').length,x:Math.min(innerWidth-30,rect.left+rect.width/2),y:Math.min(innerHeight-30,rect.top+rect.height/2)};
      });
      await page.evaluate(()=>{
        window.__scrollFrames=[];window.__scrollSampling=true;let previous;
        const step=now=>{if(previous!==undefined)window.__scrollFrames.push(now-previous);previous=now;if(window.__scrollSampling)requestAnimationFrame(step);};
        requestAnimationFrame(step);
      });
      const distance=targetInfo.scrollHeight-targetInfo.clientHeight;
      await session.send('Input.synthesizeScrollGesture',{x:targetInfo.x,y:targetInfo.y,yDistance:-distance,speed:Math.ceil(distance/3),gestureSourceType:'mouse'});
      const durations=await page.evaluate(()=>{window.__scrollSampling=false;return window.__scrollFrames;});
      const frames={...targetInfo,durations};
      const sorted=[...frames.durations].sort((a,b)=>a-b);
      result.samples.push({viewport,rate,...frames,frameP95:sorted[Math.ceil(sorted.length*.95)-1],frameMax:Math.max(...sorted)});
    }
  }
  await session.send('Emulation.setCPUThrottlingRate',{rate:1});
  await page.setViewportSize({width:1440,height:1000});
  return result;
  } finally {
    await page.evaluate(()=>{window.__scrollSampling=false;}).catch(()=>{});
    await session.send('Emulation.setCPUThrottlingRate',{rate:1}).catch(()=>{});
    await session.detach().catch(()=>{});
  }
}
