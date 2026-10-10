import {createRequire} from 'node:module';
import {networkInterfaces} from 'node:os';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

async function fixture(page,options={}){
 let state={initialized:false,agent_id:'agent',mode:'running',version:1,activation_epoch:0,busy:options.busy?['inputs','tools']:[]};
 const requests=[],receipts=new Map();let lose=options.lose;
 await page.route('**/api/**',async route=>{
  const request=route.request(),path=new URL(request.url()).pathname;
  const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
  if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
  if(path==='/api/config')return json({email_enabled:false});
  if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
  if(path.endsWith('/notifications'))return json({items:[],unread:0});
  if(path.endsWith('/fleet'))return json({agents:[]});
  if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',extensions:[]},can_execute:options.writable!==false});
  if(path.endsWith('/run-state'))return json(state);
  if(path.endsWith('/runtime-status'))return json({initialized:false,states:{},observed_at:'2026-10-10T00:00:00Z'});
  if(path.endsWith('/environment-status'))return json({binding:{environment_id:''},default_state:'unprovisioned',environments:[],observed_at:'2026-10-10T00:00:00Z'});
  if(path.endsWith('/observation-sources'))return json({sources:[],controls:[],targets:[]});
  if(path.endsWith('/mcp-connections'))return json({items:[]});
  if(path.endsWith('/lifecycle')){
   const body=request.postDataJSON();requests.push(body);
   if(!receipts.has(body.request_id)){
    const deferred=state.busy.length&&!body.interrupt&&body.action!=='resume';
    if(!deferred)state={...state,initialized:true,mode:body.action==='pause'?'paused':'running',version:state.version+1,activation_epoch:state.activation_epoch+1};
    receipts.set(body.request_id,{request_id:body.request_id,action:body.action,outcome:deferred?'deferred':'applied',state});
   }
   if(lose){lose=false;return route.abort('connectionreset');}
   return json(receipts.get(body.request_id));
  }
  return route.fulfill({status:404,body:'{}'});
 });
 await page.goto(`${options.origin??''}/t/tenant/agents/agent/runtime`);
 return {requests,region:page.getByRole('region',{name:'Agent 运行控制'})};
}

test('Busy lifecycle defers explicitly; recovery preserves a lost-response request identity',async({page})=>{
 const {region,requests}=await fixture(page,{busy:true,lose:true});
 await expect(region).toContainText('未完成输入、工具操作');
 await region.getByRole('button',{name:'暂停运行',exact:true}).click();
 await expect(region).toContainText('操作结果尚未确认');
 await expect(region.getByRole('button',{name:'暂停运行',exact:true})).toBeDisabled();
 await region.getByRole('button',{name:'重试同一操作',exact:true}).click();
 await expect(region).toContainText('本次操作未执行');
 expect(requests).toHaveLength(2);expect(requests[0]).toEqual(requests[1]);
 await region.getByRole('checkbox').check();
 await region.getByRole('button',{name:'暂停运行',exact:true}).click();
 await expect(region).toContainText('已暂停模型调度与新工作接纳');
 expect(requests[2].request_id).not.toBe(requests[0].request_id);expect(requests[2].interrupt).toBe(true);
 await region.getByRole('button',{name:'恢复运行',exact:true}).click();
 await expect(region).toContainText('已恢复运行');
 expect(requests[3].interrupt).toBe(false);expect(requests[3].version).toBe(2);
 await page.setViewportSize({width:390,height:844});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('Read-only Agent cannot change processing state',async({page})=>{
 const {region,requests}=await fixture(page,{writable:false});
 await expect(region.getByRole('button',{name:'暂停运行',exact:true})).toBeDisabled();
 await expect(region.getByRole('button',{name:'重建运行租约',exact:true})).toBeDisabled();
 await expect(region.getByRole('checkbox')).toBeDisabled();
 expect(requests).toHaveLength(0);
});

test('Lifecycle controls work in an insecure LAN HTTP browser context',async({page})=>{
 const address=Object.values(networkInterfaces()).flat().find(value=>value?.family==='IPv4'&&!value.internal)?.address;
 expect(address,'a non-loopback interface is required').toBeTruthy();
 const {region,requests}=await fixture(page,{origin:`http://${address}:4173`});
 expect(await page.evaluate(()=>({secure:isSecureContext,uuid:typeof crypto.randomUUID}))).toEqual({secure:false,uuid:'undefined'});
 await region.getByRole('button',{name:'暂停运行',exact:true}).click();
 await expect(region).toContainText('已暂停模型调度与新工作接纳');
 await region.getByRole('button',{name:'恢复运行',exact:true}).click();
 await expect(region).toContainText('已恢复运行');
 await region.getByRole('button',{name:'重建运行租约',exact:true}).click();
 await expect(region).toContainText('已重建 Agent 的运行租约');
 expect(requests.map(request=>request.action)).toEqual(['pause','resume','restart']);
 expect(new Set(requests.map(request=>request.request_id)).size).toBe(3);
 for(const request of requests)expect(request.request_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});
