import { createRequire } from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

const id=n=>`00000000-0000-4000-8000-${String(n).padStart(12,'0')}`;
async function fixture(page){
 const user={id:'owner',email:'owner@example.test',email_verified:true};
 const thread={id:id(1000),agent_id:'agent',kind:'main',name:'Main',retention:'active',state:'idle',generation:1,sequence:650,pending_inputs:0,held_inputs:0};
 const state={scope:id(1001),checked:false,disabled:false,denied:false,calls:[]};
 const events=Array.from({length:650},(_,n)=>({id:id(n+2000),thread_id:thread.id,sequence:n+1,generation:1,kind:'message.appended',created_at:'2026-10-10T01:00:00Z',data:{id:id(n+1),role:'user',blocks:[{type:'text',text:`Request ${n+1}`}]}}));
 await page.route('**/api/**',async route=>{
  const url=new URL(route.request().url()),path=url.pathname;
  const json=v=>route.fulfill({contentType:'application/json',body:JSON.stringify(v)});
  if(path==='/api/auth/session')return json(user);
  if(path==='/api/config')return json({email_enabled:false});
  if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
  if(path.endsWith('/notifications'))return json({items:[],unread:0});
  if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',status:'active',worker_depth:2},owner_id:user.id,can_execute:true});
  if(path.endsWith('/threads'))return json([thread]);
  if(path.endsWith('/events')){
   const before=Number(url.searchParams.get('before')??0),after=Number(url.searchParams.get('after')??0);
   const rows=url.searchParams.has('before')?events.filter(e=>!before||e.sequence<before).slice(-200):events.filter(e=>e.sequence>after).slice(0,200);
   return json({thread,events:rows,next_sequence:650,previous_sequence:rows[0]?.sequence??0,has_previous:rows[0]?.sequence>1});
  }
  if(path.endsWith('/input-checks')){
   const body=route.request().postDataJSON();state.calls.push(body.message_ids);
   if(state.denied)return route.fulfill({status:403,body:'{}'});
   return json({scope_id:state.scope,enabled:!state.disabled,items:body.message_ids.filter(key=>key!==id(650)).map(key=>({input_id:key,message_id:key,scope_id:id(1001),accepted_order:1,delivery:'delivered',...(state.checked&&key===id(451)?{checked_at:'2026-10-10T01:01:00Z',check_action_id:id(5000),check_message_id:id(5001),tool_use_id:'check'}:{})}))});
  }
  return route.fulfill({status:404,body:'{}'});
 });
 await page.goto('/t/tenant/agents/agent');
 await expect(page.locator('[data-input-check="delivered"]')).toHaveCount(199);
 return state;
}

test('Input marks refresh old loaded pages, distinguish scope end and survive reload without inferring untracked status',async({page})=>{
 const state=await fixture(page);
 const row=page.locator('.management-message').filter({has:page.getByText('Request 451',{exact:true})});
 await expect(row).toContainText('待处理');
 await expect(page.locator('.management-message').filter({has:page.getByText('Request 650',{exact:true})}).locator('[data-input-check]')).toHaveCount(0);
 for(let i=0;i<3;i++)await page.getByRole('button',{name:'加载更早的消息',exact:true}).click();
 await expect(page.getByText('Request 1',{exact:true})).toBeVisible();
 state.checked=true;
 await expect(row).toContainText('已处理 · Agent 确认');
 await expect(row.locator('[data-input-check]')).toHaveAttribute('title',/不代表执行成功/);
 expect(state.calls.every(keys=>keys.length<=500)).toBe(true);
 expect(state.calls.some(keys=>keys.length===500)).toBe(true);
 state.scope=id(1002);state.disabled=true;
 await expect(page.locator('[data-input-check="scope-ended"]')).toHaveCount(648);
 await expect(row).toContainText('已处理 · Agent 确认 · 模块已停用');
 await page.reload();
 await expect(page.locator('[data-input-check="checked"]')).toHaveCount(1);
 await expect(page.locator('[data-input-check="scope-ended"]')).toHaveCount(198);
});

test('Denied checklist clears stale marks and stops its polling',async({page})=>{
 const state=await fixture(page);state.denied=true;
 await expect(page.getByText('输入处理标记暂不可用：',{exact:false})).toBeVisible();
 await expect(page.locator('[data-input-check]')).toHaveCount(0);
 const count=state.calls.length;
 await page.waitForTimeout(3500);
 expect(state.calls.length).toBe(count);
});
