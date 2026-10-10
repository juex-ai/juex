import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

async function setup(page,writable=true){
 let agent={id:'agent',name:'Assistant',version:4,configuration:{},instructions:'',extensions:[],hooks:[],worker_depth:1,dynamic_instructions:{enabled:false,global_path:''}};
 const writes=[];
 await page.route('**/api/**',async route=>{
  const request=route.request(),path=new URL(request.url()).pathname;
  const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
  if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
  if(path==='/api/config')return json({email_enabled:false});
  if(path==='/api/tenants')return json([{id:'tenant',name:'Tenant',role:'admin'}]);
  if(path.endsWith('/notifications'))return json({items:[],unread:0});
  if(path.endsWith('/fleet'))return json({agents:[]});
  if(path.endsWith('/models'))return json([]);
  if(path.endsWith('/agents/agent'))return json({agent,owner_id:'owner',can_execute:writable,layers:Object.fromEntries(['tenant','fleet','workspace','agent'].map(layer=>[layer,{version:1,declaration:{}}])),effective:{models:[],modules:{},module_sources:{}}});
  if(path.endsWith('/agent-management')){const body=request.postDataJSON();writes.push(body);agent={...agent,version:agent.version+1,agent_management:body.enabled};return json(agent)}
  return route.fulfill({status:404,body:'{}'});
 });
 await page.goto('/t/tenant/agents/agent/settings');return writes;
}

test('Agent management is explicit and separate from inherited modules',async({page})=>{
 const writes=await setup(page);
 await expect(page.getByRole('heading',{name:'管理其他 Agent',exact:true})).toBeVisible();
 await expect(page.getByText('仅人工授予，默认关闭；不随模块预设或 Workspace 继承。启用后从新一轮对话生效，撤销后旧授权不能恢复。')).toBeVisible();
 expect(writes).toHaveLength(0);
 await page.getByRole('button',{name:'授予 Agent 管理权限',exact:true}).click();
 await expect(page.getByRole('button',{name:'撤销 Agent 管理权限',exact:true})).toBeVisible();
 expect(writes).toEqual([{version:4,enabled:true}]);
 await page.getByRole('button',{name:'撤销 Agent 管理权限',exact:true}).click();
 await expect(page.getByRole('button',{name:'授予 Agent 管理权限',exact:true})).toBeVisible();
 expect(writes[1]).toEqual({version:5,enabled:false});
 await page.setViewportSize({width:390,height:844});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('Suspended ownership cannot grant Agent management',async({page})=>{
 const writes=await setup(page,false);
 await expect(page.getByRole('button',{name:'授予 Agent 管理权限',exact:true})).toBeDisabled();
 expect(writes).toHaveLength(0);
});
