import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

async function fixture(page,state){
  await page.route('**/api/**',async route=>{
    const request=route.request(),url=new URL(request.url()),path=url.pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/fleet'))return json({agents:[]});
    if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',extensions:[]},can_execute:true});
    if(path.endsWith('/runtime-status'))return json({initialized:false,states:{},observed_at:'2026-10-10T00:00:00Z'});
    if(path.endsWith('/environment-status'))return json({binding:{},default_state:'unprovisioned',environments:[],observed_at:'2026-10-10T00:00:00Z'});
    if(path.endsWith('/mcp-connections')){
      state.reads++;
      return json({items:[{id:'connection',environment_id:'environment',environment:'Mac execution',state:state.offline?'unconfirmed':state.connecting?'connecting':'running',observed_state:'running',can_refresh:!state.offline&&!state.connecting,created_at:'2026-10-10T00:00:00Z',binding_id:'binding',directory:'/workspace/mcp',handshake:state.connecting?null:{transport:'http',server_name:'Workspace tools',server_version:'1',protocol_version:'2025-11-25',connected_at:'2026-10-10T00:00:00Z'},latest_tools:state.list}],next:'',observed_at:'2026-10-10T00:00:00Z'});
    }
    if(path.endsWith('/mcp-connections/connection/tools')){
      const change=request.postDataJSON();state.posts.push(change);
      if(state.rejection)return route.fulfill({status:state.rejection,body:'{}'});
      state.list={id:change.id,state:state.posts.length===1?'running':'completed',requested_at:'2026-10-10T00:00:00Z',cursor:change.cursor,next_cursor:change.cursor?'':'page-two',tools:state.posts.length===1?[]:[{name:change.cursor?'second_tool':'first_tool',description:'Tool description',input_schema:{type:'object'}}],output_expired:false,incomplete:false};
      if(state.posts.length===1)return route.fulfill({status:503,body:'{}'});
      return json(state.list);
    }
    if(path.endsWith('/output')){state.checks++;return json({state:'running',output:'',output_bytes:0,next_cursor:0,output_expired:false});}
    return route.fulfill({status:404,body:'{}'});
  });
}

test('MCP status reads have no protocol side effects; an unknown refresh survives reload with its original identity',async({page})=>{
  const state={reads:0,posts:[],checks:0,list:null,offline:false};
  await fixture(page,state);
  await page.goto('/t/tenant/agents/agent/runtime');
  const panel=page.getByRole('region',{name:'MCP 实际连接'});
  await expect(panel).toContainText('Workspace tools');
  await expect(panel).toContainText('Mac execution · http');
  await expect(panel).toContainText('尚未读取此连接的工具列表');
  expect(state.posts).toHaveLength(0);
  await panel.getByRole('button',{name:'刷新工具列表',exact:true}).click();
  await expect(panel.getByRole('button',{name:'检查原刷新结果',exact:true})).toBeVisible();
  const original=state.posts[0].id;
  await page.reload();
  await expect(panel.getByRole('button',{name:'刷新工具列表',exact:true})).toBeDisabled();
  expect(state.posts).toHaveLength(1);
  await panel.getByRole('button',{name:'检查原刷新结果',exact:true}).click();
  await expect(panel).toContainText('原刷新请求仍在执行');
  expect(state.posts).toHaveLength(1);expect(state.checks).toBe(1);
  await panel.getByRole('button',{name:'重试原刷新请求',exact:true}).click();
  await expect(panel).toContainText('first_tool');
  expect(state.posts.map(value=>value.id)).toEqual([original,original]);
  await panel.getByText('first_tool · Tool description',{exact:true}).click();
  await expect(panel.locator('pre')).toContainText('object');
  await panel.getByRole('button',{name:'读取下一页工具',exact:true}).click();
  await expect(panel).toContainText('second_tool');
  expect(state.posts[2].cursor).toBe('page-two');expect(state.posts[2].id).not.toBe(original);
  state.offline=true;await page.reload();
  await expect(panel).toContainText('连接待确认');
  await expect(panel.getByRole('button',{name:'刷新工具列表',exact:true})).toBeDisabled();
  await expect(panel).toContainText('2025-11-25');
});

test('MCP list expiration and storage failure remain explicit and never trigger an implicit refresh',async({page})=>{
  await page.addInitScript(()=>{
    const set=Storage.prototype.setItem;
    Storage.prototype.setItem=function(key,value){if(key.startsWith('juex.mcp-refresh:'))throw new Error('Request storage unavailable');return set.call(this,key,value);};
  });
  const state={reads:0,posts:[],checks:0,offline:false,list:{id:'expired',state:'completed',requested_at:'2026-10-10T00:00:00Z',tools:[],cursor:'',next_cursor:'',output_expired:true,incomplete:false}};
  await fixture(page,state);
  await page.goto('/t/tenant/agents/agent/runtime');
  const panel=page.getByRole('region',{name:'MCP 实际连接'});
  await expect(panel).toContainText('工具列表已过保留期');
  await panel.getByRole('button',{name:'刷新工具列表',exact:true}).click();
  await expect(panel).toContainText('Request storage unavailable');
  expect(state.posts).toHaveLength(0);
});

for(const rejection of [403,409])test(`Rejected MCP retry ${rejection} preserves the original request across reload`,async({page})=>{
 const state={reads:0,posts:[],checks:0,list:null,offline:false};
 await fixture(page,state);await page.goto('/t/tenant/agents/agent/runtime');
 const panel=page.getByRole('region',{name:'MCP 实际连接'});
 await panel.getByRole('button',{name:'刷新工具列表',exact:true}).click();
 await expect(panel.getByRole('button',{name:'检查原刷新结果',exact:true})).toBeVisible();
 const original=state.posts[0].id;
 state.rejection=rejection;await page.reload();
 await panel.getByRole('button',{name:'重试原刷新请求',exact:true}).click();
 await expect.poll(()=>state.posts.length).toBe(2);
 await expect(panel.getByRole('button',{name:'重试原刷新请求',exact:true})).toBeEnabled();
 expect(state.posts[1].id).toBe(original);
 state.offline=true;await page.reload();
 await panel.getByRole('button',{name:'检查原刷新结果',exact:true}).click();
 await expect(panel).toContainText('原刷新请求仍在执行');
 expect(state.checks).toBe(1);expect(state.posts).toHaveLength(2);
 await expect(panel.getByRole('button',{name:'重试原刷新请求',exact:true})).toBeDisabled();
});

test('A running executor does not imply a completed MCP handshake',async({page})=>{
 const state={reads:0,posts:[],checks:0,list:null,offline:false,connecting:true};
 await fixture(page,state);await page.goto('/t/tenant/agents/agent/runtime');
 const panel=page.getByRole('region',{name:'MCP 实际连接'});
 await expect(panel).toContainText('正在握手');
 await expect(panel).toContainText('握手未完成');
 await expect(panel.getByRole('button',{name:'刷新工具列表',exact:true})).toBeDisabled();
 expect(state.posts).toHaveLength(0);
});
