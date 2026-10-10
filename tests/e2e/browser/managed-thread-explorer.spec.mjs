import { createRequire } from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

test('Thread Explorer preserves ownership, orders bulk lifecycle and reports partial failures',async({page})=>{
  const user={id:'owner',email:'owner@example.test',email_verified:true};
  const main={id:'main',agent_id:'agent',kind:'main',name:'Main',retention:'active',state:'idle',generation:1,sequence:0,pending_inputs:0,held_inputs:0,updated_at:'2026-10-10T01:00:00Z'};
  const threads=[main,{...main,id:'parent',kind:'worker',parent_id:'main',name:'Parent'},{...main,id:'child',kind:'worker',parent_id:'parent',name:'Child'},{...main,id:'busy',kind:'worker',parent_id:'main',name:'Busy'},{...main,id:'calendar',kind:'worker',parent_id:'main',name:'Scheduled',application:'calendar'}];
  const calls=[];
  await page.route('**/api/**',async route=>{
    const url=new URL(route.request().url()),path=url.pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',status:'active',worker_depth:2},owner_id:user.id,can_execute:true});
    if(path.endsWith('/threads'))return json(threads);
    if(path.endsWith('/events'))return json({thread:main,events:[],next_sequence:0});
    if(path.endsWith('/archive')){
      const id=path.split('/').at(-2),archived=route.request().postDataJSON().archived;
      calls.push({id,archived});
      if(id==='busy')return route.fulfill({status:409,body:'{}'});
      const thread=threads.find(item=>item.id===id);thread.retention=archived?'archived':'active';
      if(id==='child')return route.fulfill({status:503,body:'{}'});
      return json(thread);
    }
    if(path.endsWith('/inspection'))return json({thread:main,state:{revision:1,notes:{content:''},tasks:[]},capabilities:{disabled:[]},usage:{input_tokens:20,output_tokens:10,cached_input_tokens:0,attempts:1,reported:1,partial:0,unknown:0}});
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent');
  await page.getByRole('button',{name:'Threads',exact:true}).click();
  await expect(page.getByLabel('选择 Main',{exact:true})).toHaveCount(0);
  await expect(page.getByLabel('选择 Scheduled',{exact:true})).toHaveCount(0);
  await expect(page.getByText('父对话：Parent',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'查看 Main 状态与用量'}).click();
  await expect(page.getByRole('tabpanel')).toContainText('输入 tokens（已报告）20');
  await page.getByRole('dialog').filter({has:page.getByRole('heading',{name:'状态与上下文 · Main'})}).getByRole('button',{name:'Close',exact:true}).click();
  await page.getByRole('button',{name:'全选 Workers',exact:true}).click();
  await page.getByRole('button',{name:'归档所选 (3)',exact:true}).click();
  await expect(page.getByText('已归档 2 个 Worker；1 个未完成，仍保持选中。',{exact:true})).toBeVisible();
  await expect(page.getByLabel('选择 Busy',{exact:true})).toBeChecked();
  expect(calls.slice(0,3).map(value=>value.id)).toEqual(['child','parent','busy']);
  await page.getByLabel('查看已归档 Workers').check();
  await page.getByRole('button',{name:'全选 Workers',exact:true}).click();
  await page.getByRole('button',{name:'恢复所选 (2)',exact:true}).click();
  await expect(page.getByText('已恢复 2 个 Worker。历史记录保留。',{exact:true})).toBeVisible();
  expect(calls.slice(3)).toEqual([{id:'parent',archived:false},{id:'child',archived:false}]);
  await page.getByLabel('查看已归档 Workers').uncheck();
  await page.getByLabel('搜索 Thread 名称或标识').fill('child');
  await expect(page.getByRole('button',{name:/Child Worker/})).toBeVisible();
  await expect(page.getByRole('button',{name:/Parent Worker/})).toHaveCount(0);
});

test('Unconfirmed lifecycle survives failed refresh, drawer close and reload without resending',async({page})=>{
  const user={id:'owner',email:'owner@example.test',email_verified:true};
  const main={id:'main',agent_id:'agent',kind:'main',name:'Main',retention:'active',state:'idle',generation:1,sequence:0,pending_inputs:0,held_inputs:0};
  const worker={...main,id:'worker',kind:'worker',parent_id:'main',name:'Uncertain'};
  let failReads=false,failedReads=0,writes=0;
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',status:'active'},owner_id:user.id,can_execute:true});
    if(path.endsWith('/threads')){if(failReads){failedReads++;return route.fulfill({status:503,body:'{}'});}return json([main,worker]);}
    if(path.endsWith('/events'))return json({thread:main,events:[],next_sequence:0});
    if(path.endsWith('/archive')){writes++;worker.retention='archived';failReads=true;return route.fulfill({status:503,body:'{}'});}
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent');
  await page.getByRole('button',{name:'Threads',exact:true}).click();
  await page.getByLabel('选择 Uncertain',{exact:true}).check();
  await page.getByRole('button',{name:'归档所选 (1)',exact:true}).click();
  await expect.poll(()=>failedReads).toBeGreaterThanOrEqual(2);
  failReads=false;
  await page.getByRole('alert').getByRole('button',{name:'重试',exact:true}).click();
  // Parent failure unmounted the Sheet. The receipt remains available.
  await expect(page.getByRole('button',{name:'检查未确认结果',exact:true})).toBeVisible();
  await page.getByRole('dialog').getByRole('button',{name:'Close',exact:true}).click();
  await page.getByRole('button',{name:'Threads',exact:true}).click();
  await expect(page.getByRole('button',{name:'检查未确认结果',exact:true})).toBeVisible();
  await page.reload();
  await page.getByRole('button',{name:'Threads',exact:true}).click();
  await expect(page.getByText('Uncertain 的归档请求尚未确认。',{exact:false})).toBeVisible();
  await page.getByRole('button',{name:'检查未确认结果',exact:true}).click();
  await expect(page.getByText('已确认归档 1 个 Worker；0 个尚未处于目标状态，可刷新后重新选择。',{exact:true})).toBeVisible();
  expect(writes).toBe(1);
});
