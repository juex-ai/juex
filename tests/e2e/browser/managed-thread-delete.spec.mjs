import { createRequire } from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

async function fixture(page,current=''){
 const user={id:'owner',email:'owner@example.test',email_verified:true};
 const main={id:'main',agent_id:'agent',kind:'main',name:'Main',retention:'active',state:'idle',generation:1,sequence:0,pending_inputs:0,held_inputs:0};
 const threads=[main,{...main,id:'parent',kind:'worker',parent_id:'main',name:'Parent',retention:'archived'},{...main,id:'child',kind:'worker',parent_id:'parent',name:'Child',retention:'archived'},{...main,id:'busy',kind:'worker',parent_id:'main',name:'Busy',retention:'archived'},{...main,id:'calendar',kind:'worker',parent_id:'main',name:'Scheduled',application:'calendar',retention:'archived'}];
 const state={calls:[],uncertain:true,denied:false};
 await page.route('**/api/**',async route=>{
  const path=new URL(route.request().url()).pathname;
  const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
  if(path==='/api/auth/session')return json(user);
  if(path==='/api/config')return json({email_enabled:false});
  if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
  if(path.endsWith('/notifications'))return json({items:[],unread:0});
  if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',status:'active',worker_depth:2},owner_id:user.id,can_execute:true});
  if(path.endsWith('/threads'))return json(threads);
  if(path.endsWith('/events'))return json({thread:threads.find(thread=>thread.id===path.split('/').at(-2))??main,events:[],next_sequence:0});
  if(route.request().method()==='DELETE'){
   const id=path.split('/').at(-1);state.calls.push(id);
   if(state.denied)return route.fulfill({status:403,body:'{}'});
   if(id==='busy')return route.fulfill({status:409,body:'{}'});
   const at=threads.findIndex(thread=>thread.id===id);if(at>=0)threads.splice(at,1);
   if(state.uncertain&&id==='child')return route.fulfill({status:503,body:'{}'});
   return json({thread_id:id,deleted:true});
  }
  return route.fulfill({status:404,body:'{}'});
 });
 await page.goto('/t/tenant/agents/agent'+(current?`?thread=${current}`:''));
 await page.getByRole('button',{name:'Threads',exact:true}).click();
 await expect(page.getByRole('button',{name:/永久删除所选/})).toHaveCount(0);
 await page.getByLabel('查看已归档 Workers').check();
 await expect(page.getByLabel('选择 Scheduled')).toHaveCount(0);
 return state;
}

test('Permanent deletion confirms scope, orders children first and retains uncertain identities after disappearance/reload',async({page})=>{
 const state=await fixture(page);
 await page.getByRole('button',{name:'全选 Workers',exact:true}).click();
 await page.getByRole('button',{name:'永久删除所选 (3)',exact:true}).click();
 const dialog=page.getByRole('dialog').filter({has:page.getByRole('heading',{name:'永久删除 3 个 Worker',exact:true})});
 await expect(dialog).toContainText('此操作不可撤销');
 await expect(dialog).toContainText('独立产物、工作目录文件、已共享 Memory 及用量账本保留');
 await expect(dialog.getByRole('button',{name:'取消',exact:true})).toBeFocused();
 expect(state.calls).toEqual([]);
 await dialog.getByRole('button',{name:'确认永久删除',exact:true}).click();
 await expect(page.getByRole('button',{name:'重试原删除请求',exact:true})).toBeVisible();
 expect(state.calls).toEqual(['child','parent','busy']);
 await expect(page.getByLabel('选择 Busy')).toBeChecked();
 await expect(page.getByText('Child 的永久删除请求尚未确认。',{exact:false})).toBeVisible();
 await page.getByRole('dialog').getByRole('button',{name:'Close',exact:true}).click();
 await page.reload();
 await page.getByRole('button',{name:'Threads',exact:true}).click();
 await expect(page.getByRole('button',{name:'重试原删除请求',exact:true})).toBeVisible();
 state.uncertain=false;state.denied=true;
 await page.getByRole('button',{name:'重试原删除请求',exact:true}).click();
 await expect(page.getByText('Child 的永久删除请求尚未确认。',{exact:false})).toBeVisible();
 state.denied=false;
 await page.getByRole('button',{name:'重试原删除请求',exact:true}).click();
 await expect(page.getByText('已永久删除 1 个 Worker。',{exact:true})).toBeVisible();
 expect(state.calls).toEqual(['child','parent','busy','child','child']);
});

test('Deleting the current Worker returns to Main while keeping partial failure and selection visible',async({page})=>{
 const state=await fixture(page,'parent');state.uncertain=false;
 await page.getByRole('button',{name:'全选 Workers',exact:true}).click();
 await page.getByRole('button',{name:'永久删除所选 (3)',exact:true}).click();
 await page.getByRole('button',{name:'确认永久删除',exact:true}).click();
 await expect(page).toHaveURL(/\/t\/tenant\/agents\/agent$/);
 await expect(page.getByText('已永久删除 2 个 Worker。 1 个未完成，仍保持选中。',{exact:true})).toBeVisible();
 await expect(page.getByLabel('选择 Busy')).toBeChecked();
 await expect(page.getByRole('alert')).toContainText('Busy：请确认已归档');
 expect(state.calls).toEqual(['child','parent','busy']);
});

test('Deletion never mutates when its durable original targets cannot be saved',async({page})=>{
 await page.addInitScript(()=>{const original=Storage.prototype.setItem;Storage.prototype.setItem=function(key,value){if(key.startsWith('juex.thread-lifecycle:'))throw new Error('blocked storage');return original.call(this,key,value)}});
 const state=await fixture(page);
 await page.getByLabel('选择 Child').check();
 await page.getByRole('button',{name:'永久删除所选 (1)',exact:true}).click();
 await page.getByRole('button',{name:'确认永久删除',exact:true}).click();
 expect(state.calls).toEqual([]);
 await expect(page.getByRole('alert')).toContainText('无法保存原删除请求');
});
