async (page) => {
  const user = { id:1, email:'fixture@example.invalid', username:'fixture', role:'admin', status:'active', balance:0, concurrency:5 };
  const stamp = new Date().toISOString();
  const window = {sample_count:120,first_token_sample_count:120,successful_request_count:120,failed_request_count:0,success_rate:1,average_first_token_ms:500,average_duration_ms:2500,cache_rate:0.8,cache_rate_numerator:800,cache_rate_denominator:1000,quality_score:94,quality_grade:'S',score_basis:'ttft_duration_cache'};
  const quality = { recent_1h:window, recent_24h:window, activity:{state:'active', successful_request_count:120,failed_request_count:0,last_success_at:stamp,last_error_at:null},score_version:5};
  const accounts = Array.from({length:200},(_,i) => ({id:i+1,name:`fixture-account-${String(i+1).padStart(3,'0')}`,platform:'openai',type:'apikey',status:'active',schedulable:true,concurrency:10,current_concurrency:1,load_factor:1,priority:50,rate_multiplier:1,upstream_billing_rate:1,group_ids:[1],groups:[{id:1,name:'fixture-group',platform:'openai'}],credentials:{},extra:{},proxy_id:null,created_at:stamp,updated_at:stamp,last_used_at:stamp,upstream_config_id:1,upstream_key_id:i+1001,upstream_config_name:'fixture-provider',upstream_key_name:`key-${i+1}`,upstream_key_masked:'sk-fixture***',upstream_health:{key_id:i+1001,status:'healthy',observation_enabled:true,consecutive_failures:0,last_probe_at:stamp,last_evidence_at:stamp,last_probe_status:'success',last_traffic_status:'success',updated_at:stamp,history:Array.from({length:24},(_,j)=>({id:i*100+j,state:'healthy',source:'traffic',result:'success',model:'fixture-model',reason:'traffic_success',observed_at:new Date(Date.parse(stamp)-j*60000).toISOString(),ttft_ms:500,duration_ms:2500}))}}));
  const fixture = {accounts,quality,requests:[],errors:[],mode:'unchanged',delay:0,version:0,pageErrors:[],expectedFailureErrors:[],expectFailures:false};
  page.__upstreamFixture = fixture;
  page.on('pageerror', error => {fixture.pageErrors.push(String(error));fixture.errors.push(String(error));});
  page.on('console', message => { if(message.type()==='error') (fixture.expectFailures?fixture.expectedFailureErrors:fixture.errors).push(message.text()); });
  await page.unroute('**/api/**');
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url());
    const pathname = url.pathname;
    fixture.requests.push({path:pathname,query:url.search,at:Date.now()});
    let data = {};
    if(pathname.endsWith('/auth/me')) data=user;
    else if(pathname.endsWith('/settings/public')) data={site_name:'Local fixture',registration_enabled:false};
    else if(pathname.endsWith('/admin/upstream-management/accounts')) {
      if(fixture.delay) await new Promise(resolve=>setTimeout(resolve,fixture.delay));
      const pageNumber=Number(url.searchParams.get('page')||1), pageSize=Number(url.searchParams.get('page_size')||20);
      const search=url.searchParams.get('search')||'';
      const filtered=fixture.accounts.filter(a=>a.name.includes(search));
      const items=filtered.slice((pageNumber-1)*pageSize,pageNumber*pageSize).map(a=>({...a}));
      data={items,total:filtered.length,page:pageNumber,page_size:pageSize,pages:Math.ceil(filtered.length/pageSize)};
      if(url.searchParams.get('include_quality')==='1') {data.quality_stats=Object.fromEntries(items.map(a=>[a.id,fixture.quality]));data.quality_stats_status='ok';}
      if(fixture.mode==='quality-fail') {delete data.quality_stats;data.quality_stats_status='unavailable';}
      if(fixture.mode==='fail') return route.fulfill({status:503,json:{code:503,message:'fixture unavailable'}});
      if(fixture.beforeFulfill) await fixture.beforeFulfill();
      return route.fulfill({status:200,json:{code:0,data},headers:{'ETag':`"fixture-${fixture.version}"`,'X-Account-Evaluated-At':new Date().toISOString()}}).catch(()=>{});
    }
    else if(pathname.endsWith('/admin/upstream-management/settings')) data={scheduling_priority:'cache_first',scheduling_protection_platforms:['openai'],ttft_guard:{enabled:true,degradation_ttft_seconds:10,min_samples:3},cache_guard:{mode:'protect',group_ids:[1],threshold_percent:50,window_minutes:5,min_valid_requests:3,recovery_threshold_percent:60,cooldown_minutes:5,trial_percent:10},probe_guard:{enabled:true,suspend_after_failures:3,recovery_successes:3,custom_error_codes_enabled:false,custom_error_codes:[]},probe_models:{openai:'fixture-model'},probe_interval_seconds:60,confidence_probe:{enabled:false,reasoning_effort:'low',long_context_enabled:false,long_context_max_tokens:0,quality_degrade_threshold:0,prompt_version:'fixture'}};
    else if(pathname.endsWith('/probe-model-candidates')) data={candidates:{openai:['fixture-model']},platforms:[]};
    else if(/\/keys\/\d+\/events$/.test(pathname)) data={items:[],total:0,health_history:accounts[0].upstream_health.history};
    else if(pathname.endsWith('/health-trend')) data={key_id:Number(pathname.split('/').at(-2)),range:url.searchParams.get('range')||'24h',start_at:stamp,end_at:stamp,bucket_seconds:3600,points:[]};
    else if(pathname.includes('/groups')) data=[{id:1,name:'fixture-group',platform:'openai',status:'active'}];
    else if(pathname.includes('/proxies')||pathname.includes('/announcements')) data=[];
    else if(pathname.includes('batch')||pathname.includes('/usage')) data={};
    await route.fulfill({status:200,json:{code:0,data}}).catch(()=>{});
  });
  const autoRefresh = page.__upstreamFixtureAutoRefresh === true;
  await page.addInitScript(({user, autoRefresh}) => {
    localStorage.setItem('auth_token','fixture-token'); localStorage.setItem('auth_user',JSON.stringify(user));localStorage.setItem('locale','zh-CN');
    localStorage.setItem('table-page-size','20');
    localStorage.setItem('upstream-account-auto-refresh',JSON.stringify({enabled:autoRefresh,interval_seconds:5}));
    localStorage.setItem('upstream-account-hidden-columns',JSON.stringify(['proxy','notes','scheduler_score','rate_multiplier']));
    localStorage.setItem('upstream-account-hidden-columns-version','health-quality-today-visible-v1');
  },{user, autoRefresh});
  await page.setViewportSize({width:1440,height:1000});
  await page.goto('http://127.0.0.1:4319/admin/upstream/accounts');
  await page.getByText('fixture-account-001',{exact:true}).waitFor();
  return {fixture:true,url:page.url(),requests:fixture.requests,errors:fixture.errors};
}
