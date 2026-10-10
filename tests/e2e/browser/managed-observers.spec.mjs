import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');
const initial=()=>({posts:[],stops:0,subscriptions:[],reads:0,sources:[],controls:[],version:2,revision:'saved-catalog',reject:0,disabled:false});
async function fixture(page,state){
 await page.route('**/api/**',async route=>{
 const request=route.request(),url=new URL(request.url()),path=url.pathname;
 const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
 if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
 if(path==='/api/config')return json({email_enabled:false});
 if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
 if(path.endsWith('/notifications'))return json({items:[],unread:0});
 if(path.endsWith('/fleet'))return json({agents:[]});
 if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',version:state.version,extensions:[{id:'binding',enabled:true,resources:['observable/watch'],environment_id:'environment',directory:'/workspace/watcher',catalog:{revision:state.revision,manifest:{name:'Workspace observer',version:'1',skills:[],mcp:[],hooks:[],observables:[{id:'watch',description:'Watch updates'}]}}}]},can_execute:!state.disabled,effective:{modules:{observations:{enabled:!state.disabled}}}});
 if(path.endsWith('/runtime-status'))return json({initialized:true,main_thread_id:'main',states:{idle:1},observed_at:'2026-10-10T00:00:00Z'});
 if(path.endsWith('/environment-status'))return json({binding:{},default_state:'unprovisioned',environments:[],observed_at:'2026-10-10T00:00:00Z'});
 if(path.endsWith('/mcp-connections'))return json({items:[],next:'',observed_at:'2026-10-10T00:00:00Z'});
 if(path.endsWith('/observation-sources')){state.reads++;return json({sources:state.sources,controls:state.controls,targets:[{id:'main',name:'Main'},{id:'worker',name:'Worker'}],next:'',targets_truncated:false});}
 if(path.endsWith('/observers')){
 const body=request.postDataJSON();state.posts.push(body);
 if(state.reject)return route.fulfill({status:state.reject,body:'{}'});
 if(state.posts.length===1)return route.fulfill({status:503,body:'{}'});
 const control={...body,id:'controller',source_id:'source',state:'pending',attempt:1,desired:'running',created_at:'2026-10-10T00:00:00Z'};
 state.controls=[control];state.sources=[{id:'source',thread_id:body.thread_id,control_id:'controller',origin:'manual',environment_id:'environment',operation_id:'source',kind:'observe_command',directory:'/workspace/watcher',cursor:0,closed:false,stop_requested:false,subscriptions:[]}];return json(control);
 }
 if(path.endsWith('/observation-sources/source/events'))return json({items:[{id:'event',kind:'command.observation',data:{text:'first event'},created_at:'2026-10-10T00:00:00Z',pending:0,delivered:1,skipped:0,data_truncated:false}],next:''});
 if(path.endsWith('/observation-sources/source/stop')){state.stops++;state.sources[0].stop_requested=true;return json({stop_requested:true});}
 if(path.includes('/subscriptions/')){state.subscriptions.push({path,body:request.postDataJSON()});return json({});}
 if(path.endsWith('/output'))return json({state:'unknown',output:'',next_cursor:0,output_bytes:0,output_expired:false});
 return route.fulfill({status:404,body:'{}'});
 });
}
test('Manual observer start survives response loss and a configuration revision across reload',async({page})=>{
 const state=initial();await fixture(page,state);await page.goto('/t/tenant/agents/agent/runtime');
 const panel=page.getByRole('region',{name:'事件源与订阅'});
 await expect(panel).toContainText('/workspace/watcher');expect(state.posts).toHaveLength(0);
 await panel.getByLabel('运行方式',{exact:true}).selectOption('continuous');
 await panel.getByLabel('所属 Thread',{exact:true}).selectOption('worker');
 await panel.getByRole('button',{name:'启动资源',exact:true}).click();
 await expect(panel.getByRole('button',{name:'核对并重试原启动'})).toBeVisible();
 const original=state.posts[0];expect(original.mode).toBe('continuous');expect(original.thread_id).toBe('worker');expect(original.subscribe).toBe(true);
 state.version=9;state.revision='new-catalog';state.reject=409;await page.reload();
 await panel.getByRole('button',{name:'核对并重试原启动'}).click();
 await expect.poll(()=>state.posts.length).toBe(2);
 await expect(panel.getByRole('button',{name:'核对并重试原启动'})).toBeEnabled();
 await page.reload();state.reject=0;
 await panel.getByRole('button',{name:'核对并重试原启动'}).click();
 await expect(panel).toContainText('人工启动');expect(state.posts).toEqual([original,original,original]);
 await expect(panel).toContainText('持续运行 · 第 1 次');
 // A known successful receipt must not become uncertain when the visible page no longer contains it.
 state.controls=[];state.sources=[];await page.reload();
 await expect(panel.getByRole('button',{name:'启动资源',exact:true})).toBeEnabled();
 await expect(panel.getByRole('button',{name:'核对并重试原启动'})).toHaveCount(0);
});

test('Observer events distinguish delivery from processing; stopping keeps the original unknown result visible',async({page})=>{
 const state=initial();state.sources=[{id:'source',thread_id:'main',control_id:'',origin:'model',environment_id:'original-environment',operation_id:'original-operation',kind:'observe_command',directory:'/original/path',cursor:16,closed:false,stop_requested:false,subscriptions:[{id:'sub',thread_id:'main',enabled:true,kind:'command.observation'}]}];
 await fixture(page,state);await page.goto('/t/tenant/agents/agent/runtime');
 const source=page.getByRole('article',{name:'事件源 source'});
 await source.getByRole('button',{name:'查看事件与原操作'}).click();
 await expect(source).toContainText('已投递 1');await expect(source).toContainText('不表示模型已经处理完成');
 await expect(source).toContainText('操作结果未知');
 await source.getByRole('button',{name:'停止资源及其订阅'}).click();
 await expect(source).toContainText('停止请求已保存，等待原环境确认');
 await expect(source.getByRole('button',{name:'停止资源及其订阅'})).toBeDisabled();
 expect(state.stops).toBe(1);expect(state.posts).toHaveLength(0);
 await source.getByRole('button',{name:'取消此 Thread 订阅'}).click();
 expect(state.subscriptions).toEqual([{path:'/api/tenants/tenant/agents/agent/observation-sources/source/subscriptions/main',body:{enabled:false}}]);
});

test('Observer start never posts without saving its request identity',async({page})=>{
 await page.addInitScript(()=>{const original=Storage.prototype.setItem;Storage.prototype.setItem=function(key,value){if(key.startsWith('juex.observer-start:'))throw new Error('Request storage unavailable');return original.call(this,key,value);};});
 const state=initial();await fixture(page,state);await page.goto('/t/tenant/agents/agent/runtime');
 const panel=page.getByRole('region',{name:'事件源与订阅'});
 await panel.getByRole('button',{name:'启动资源',exact:true}).click();
 await expect(panel).toContainText('Request storage unavailable');expect(state.posts).toHaveLength(0);
 state.disabled=true;await page.reload();await expect(panel.getByRole('button',{name:'启动资源',exact:true})).toBeDisabled();
});
