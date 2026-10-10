import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

test('Runtime inspection keeps service failures distinct and never calls provisioning reads',async({page})=>{
  const requests=[];
  let unavailable=true;
  let modules={observations:{enabled:false}};
  let diagnosticsFail=false;
  await page.route('**/api/**',async route=>{
    const request=route.request(),path=new URL(request.url()).pathname;
    requests.push({method:request.method(),path});
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/fleet'))return json({agents:[{id:'agent',name:'Assistant',status:'active'}]});
    if(path.endsWith('/agents/agent'))return json({
      agent:{id:'agent',name:'Assistant',hooks:[{id:'audit',enabled:true,events:['PreToolUse'],command:['/bin/sh','-c','echo audit'],source:'workspace/juex.yaml',required:true}],extensions:[{
        id:'binding',enabled:true,environment_id:'environment',directory:'/workspace/private-extension',resources:['mcp/search','skill/guide','observable/inbox','hook/check'],
        catalog:{revision:'sha256:catalog',manifest:{name:'Workspace tools',version:'1',skills:[{id:'guide',description:'Guide'}],mcp:[{id:'search',description:'Search'}],observables:[{id:'inbox',description:'Inbox'}],hooks:[{id:'check',events:['UserPromptSubmit']}]}},
      }]},can_execute:true,effective:{modules},
    });
    if(path.endsWith('/runtime-status'))return json({initialized:true,main_thread_id:'main',active_threads:2,archived_threads:3,pending_inputs:1,held_inputs:0,states:{idle:1,waiting:1},last_activity:'2026-10-10T00:00:00Z',observed_at:'2026-10-10T00:01:00Z'});
    if(path.endsWith('/environment-status')){
      if(unavailable)return route.fulfill({status:503,body:'{}'});
      return json({binding:{environment_id:'environment',version:1},default_state:'selected',observed_at:'2026-10-10T00:01:00Z',environments:[{id:'environment',default:true,name:'Mac local',kind:'native',managed:true,os:'darwin',online:false,availability:'sleeping',working_directory:'/workspace',permission_mode:'current_os_user',capabilities:['files','shell'],authorization_version:4,last_seen:'2026-10-10T00:00:00Z'}]});
    }
    if(path.endsWith('/inspection'))return json({thread:{name:'Main',generation:1},state:{notes:{content:''},tasks:[],revision:0},capabilities:{disabled:[]},latest_request:null,usage:{input_tokens:0,output_tokens:0,cached_input_tokens:0,attempts:0,reported:0,partial:0,unknown:0}});
    if(path.endsWith('/events')){
      if(diagnosticsFail)return route.fulfill({status:503,body:'{}'});
      const older=new URL(request.url()).searchParams.get('before')==='101';
      const events=[{id:older?'old-event':'event',sequence:older?10:101,generation:1,kind:older?'turn.started':'model.fallback',created_at:'2026-10-10T00:00:00Z',turn_id:'turn',data:older?{}:{reason:'provider_error',to_model:'provider:backup',arguments:'DO-NOT-RENDER-PRIVATE-PAYLOAD'}}];
      if(!older)events.push({id:'tool-failed',sequence:102,generation:1,kind:'tool.ready',created_at:'2026-10-10T00:01:00Z',data:{call:{tool_name:'read_file',arguments:'PRIVATE-ARGUMENTS'},result:{is_error:true,content:'PRIVATE-TOOL-OUTPUT'}}});
      return json({thread:{id:'main'},events,has_previous:!older,previous_sequence:older?10:101});
    }
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent/runtime');
  const work=page.getByRole('region',{name:'Runtime 工作状态'}), location=page.getByRole('region',{name:'代码运行位置'});
  await expect(work).toContainText('等待执行结果 1');
  await expect(location.getByRole('button',{name:'重试',exact:true})).toBeVisible();
  await expect(location).not.toContainText('暂无已授权');
  await expect(page.getByRole('region',{name:'已保存的扩展资源'})).toContainText('Agent 模块已关闭');
  unavailable=false;
  await location.getByRole('button',{name:'重试',exact:true}).click();
  await expect(location).toContainText('原生目录与 Shell · darwin · 休眠');
  await expect(location).toContainText('/workspace');
  const hooks=page.getByRole('region',{name:'Agent Hooks',exact:true});
  await expect(hooks).toContainText('workspace/juex.yaml');
  await expect(hooks).toContainText('执行环境：Mac local');
  await expect(hooks).toContainText('执行工具前');
  await hooks.getByText('命令及执行规则',{exact:true}).click();
  await expect(hooks).toContainText('echo audit');
  await page.getByRole('button',{name:'查看 Main 工具与上下文',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText('本代尚无模型请求');
  await page.getByRole('button',{name:'Close',exact:true}).click();
  await page.getByRole('button',{name:'查看 Main 诊断记录',exact:true}).click();
  const diagnostics=page.getByRole('region',{name:'Thread 诊断记录',exact:true});
  await expect(diagnostics).toContainText('provider_error');
  await expect(diagnostics).toContainText('provider:backup');
  await expect(diagnostics).not.toContainText('DO-NOT-RENDER-PRIVATE-PAYLOAD');
  await diagnostics.getByLabel('只看本页异常与模型切换').check();
  await expect(diagnostics).toContainText('read_file');
  await expect(diagnostics).toContainText('工具返回错误标记');
  await expect(diagnostics).not.toContainText('PRIVATE-ARGUMENTS');
  await expect(diagnostics).not.toContainText('PRIVATE-TOOL-OUTPUT');
  await diagnostics.getByRole('button',{name:'更早记录',exact:true}).click();
  await expect(diagnostics).toContainText('本页没有匹配记录');
  await diagnostics.getByLabel('只看本页异常与模型切换').uncheck();
  await expect(diagnostics).toContainText('turn.started');
  diagnosticsFail=true;
  await diagnostics.getByRole('button',{name:'刷新最新记录',exact:true}).click();
  await expect(diagnostics.getByRole('button',{name:'重试',exact:true})).toBeVisible();
  await expect(diagnostics).not.toContainText('turn.started');
  diagnosticsFail=false;
  await diagnostics.getByRole('button',{name:'重试',exact:true}).click();
  await expect(diagnostics).toContainText('provider_error');
  await page.getByRole('button',{name:'Close',exact:true}).click();
  const resource=label=>page.getByRole('listitem').filter({has:page.getByText(label,{exact:true})});
  await expect(resource('Observable · inbox')).toContainText('Agent 模块已关闭');
  await expect(resource('Skill · guide')).toContainText('已选择');
  modules={extensions:{enabled:false},skills:{enabled:true}};await page.reload();
  for(const label of ['MCP · search','Observable · inbox','Hook · check'])await expect(resource(label)).toContainText('Agent 模块已关闭');
  await expect(resource('Skill · guide')).toContainText('已选择');
  modules={extensions:{enabled:true},skills:{enabled:false}};await page.reload();
  await expect(resource('Skill · guide')).toContainText('Agent 模块已关闭');
  for(const label of ['MCP · search','Observable · inbox','Hook · check'])await expect(resource(label)).toContainText('已选择');
  modules={shell:{enabled:false}};await page.reload();
  await expect(hooks).toContainText('Agent 模块已关闭');
  for(const label of ['Observable · inbox','Hook · check'])await expect(resource(label)).toContainText('Agent 模块已关闭');
  for(const label of ['MCP · search','Skill · guide'])await expect(resource(label)).toContainText('已选择');
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  expect(requests.every(request=>request.method==='GET')).toBe(true);
  expect(requests.some(request=>/\/(threads|environments)$/.test(request.path))).toBe(false);
});

test('Uninitialized Agent and revoked default do not imply a ready fallback',async({page})=>{
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/fleet'))return json({agents:[]});
    if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Archived',extensions:[]},can_execute:false});
    if(path.endsWith('/runtime-status'))return json({initialized:false,states:{},observed_at:'2026-10-10T00:01:00Z'});
    if(path.endsWith('/environment-status'))return json({binding:{environment_id:'revoked-device'},default_state:'unavailable',environments:[],observed_at:'2026-10-10T00:01:00Z'});
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent/runtime');
  await expect(page.getByText('尚未开始工作',{exact:true})).toBeVisible();
  await expect(page.getByText('不会自动切换到另一台设备。',{exact:false})).toBeVisible();
  await expect(page.getByRole('button',{name:'查看 Main 工具与上下文',exact:true})).toHaveCount(0);
});

test('Slow status reads settle before the next poll and their errors remain visible',async({page})=>{
  await page.clock.install();
  let pending,release,count=0;
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/fleet'))return json({agents:[]});
    if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Slow runtime',extensions:[]},can_execute:true});
    if(path.endsWith('/runtime-status')){
      count++;
      if(count===1){pending=route;await new Promise(resolve=>{release=resolve;});return;}
      return json({initialized:false,states:{},observed_at:'2026-10-10T00:01:00Z'});
    }
    if(path.endsWith('/environment-status'))return json({binding:{environment_id:''},default_state:'unprovisioned',environments:[],observed_at:'2026-10-10T00:01:00Z'});
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent/runtime');
  await expect.poll(()=>count).toBe(1);
  await page.clock.runFor(11_000);
  expect(count).toBe(1);
  await pending.fulfill({status:503,body:'{}'});release();
  await expect(page.getByRole('region',{name:'Runtime 工作状态'}).getByRole('button',{name:'重试',exact:true})).toBeVisible();
  await expect(page.getByRole('region',{name:'代码运行位置'})).toContainText('默认托管环境尚未分配');
  await page.clock.runFor(10_000);
  await expect.poll(()=>count).toBe(2);
  await expect(page.getByText('尚未开始工作',{exact:true})).toBeVisible();
});
