import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

test('Private environment layers save write-only values, bind Workspace and preserve conflicting drafts',async({page})=>{
 const user={id:'owner',email:'owner@example.test',email_verified:true};
 const tenant={id:'tenant',name:'Workspace',role:'admin'};
 const agent={id:'agent',name:'Assistant',version:1,status:'active',configuration:{},hooks:[],extensions:[]};
 let view={tenant_writable:true,layers:Object.fromEntries(['tenant','fleet','workspace','agent'].map(key=>[key,{version:0,keys:[]}])),effective:[]};
 const writes=[];let conflict=false;
 await page.route('**/api/**',async route=>{
  const path=new URL(route.request().url()).pathname;const json=v=>route.fulfill({contentType:'application/json',body:JSON.stringify(v)});
  if(path==='/api/auth/session')return json(user);
  if(path==='/api/config')return json({email_enabled:false});
  if(path==='/api/tenants')return json([tenant]);
  if(path.endsWith('/notifications'))return json({items:[],unread:0});
  if(path.endsWith('/models'))return json([]);
  if(path.endsWith('/agents/agent'))return json({agent,owner_id:'owner',can_execute:true});
  if(path.endsWith('/environment-status'))return json({environments:[{id:'device',name:'My Mac',capabilities:['shell'],working_directory:'/workspace'}]});
  if(path.endsWith('/process-environment'))return json(view);
  if(path.includes('/process-environment/')){
   const layer=path.split('/').at(-1),change=route.request().postDataJSON();writes.push({layer,...change});
   if(conflict){conflict=false;view.layers[layer].version++;return route.fulfill({status:409,body:'{}'});}
   expect(change.version).toBe(view.layers[layer].version);
   view={...view,layers:{...view.layers,[layer]:{...view.layers[layer],version:change.version+1,keys:[...view.layers[layer].keys.filter(key=>!change.remove.includes(key)&&!(key in change.set)),...Object.keys(change.set)],environment_id:change.environment_id,working_directory:change.working_directory}},effective:Object.keys(change.set).map(name=>({name,source:layer}))};return json(view);
  }
  return route.fulfill({status:404,body:'{}'});
 });
 await page.goto('/t/tenant/agents/agent/settings');
 await page.getByRole('button',{name:'配置执行环境变量',exact:true}).click();
 const dialog=page.getByRole('dialog',{name:'执行环境变量'});
 await dialog.getByLabel('变量名',{exact:true}).fill('TAVILY_API_KEY');
 await dialog.getByLabel('新值',{exact:true}).fill('private-fixture-value');
 await expect(dialog.getByLabel('新值',{exact:true})).toHaveAttribute('type','password');
 await dialog.getByRole('button',{name:'保存变量',exact:true}).click();
 await expect(dialog.getByText('环境变量已保存。页面不会回填，系统不会将私有值写入命令参数。',{exact:true})).toBeVisible();
 await expect(dialog.getByLabel('新值',{exact:true})).toHaveValue('');
 expect(writes[0].set).toEqual({TAVILY_API_KEY:'private-fixture-value'});
 await expect(dialog).not.toContainText('private-fixture-value');
 await dialog.getByLabel('配置层级',{exact:true}).selectOption('workspace');
 await expect(dialog.getByLabel('变量所在执行环境',{exact:true})).toHaveValue('device');
 await expect(dialog.getByLabel('变量工作目录',{exact:true})).toHaveValue('/workspace');
 await dialog.getByLabel('变量名',{exact:true}).fill('LARKSUITE_CLI_CONFIG_DIR');
 await dialog.getByLabel('新值',{exact:true}).fill('/workspace/private-lark');
 await dialog.getByRole('button',{name:'保存变量',exact:true}).click();
 await expect.poll(()=>writes.length).toBe(2);
 expect(writes[1]).toMatchObject({layer:'workspace',environment_id:'device',working_directory:'/workspace'});
 await dialog.getByLabel('变量名',{exact:true}).fill('OTHER');await dialog.getByLabel('新值',{exact:true}).fill('draft');conflict=true;
 await dialog.getByRole('button',{name:'保存变量',exact:true}).click();
 await expect(dialog.getByRole('button',{name:'重新加载元数据'})).toBeVisible();
 await expect(dialog.getByLabel('新值',{exact:true})).toHaveValue('draft');
 await expect(dialog.getByRole('button',{name:'保存变量',exact:true})).toBeDisabled();
 await dialog.getByRole('button',{name:'重新加载元数据'}).click();
 await expect(dialog.getByLabel('新值',{exact:true})).toHaveValue('');
 await dialog.getByRole('button',{name:'删除 LARKSUITE_CLI_CONFIG_DIR',exact:true}).click();
 await expect.poll(()=>writes.length).toBe(4);
 expect(writes[3].remove).toEqual(['LARKSUITE_CLI_CONFIG_DIR']);
 expect(writes[3].environment_id).toBeUndefined();
});

test('Revoked Workspace environment keeps saved keys removable without starting an executor',async({page})=>{
 let layer={version:2,keys:['ONE','TWO'],environment_id:'revoked',working_directory:'/saved/root'};
 const writes=[];
 await page.route('**/api/**',async route=>{
  const path=new URL(route.request().url()).pathname;const json=v=>route.fulfill({contentType:'application/json',body:JSON.stringify(v)});
  const view=()=>({tenant_writable:true,layers:{agent:{version:0,keys:[]},workspace:layer},effective:[]});
  if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
  if(path==='/api/config')return json({email_enabled:false});
  if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
  if(path.endsWith('/notifications'))return json({items:[],unread:0});
  if(path.endsWith('/models'))return json([]);
  if(path.endsWith('/agents/agent'))return json({agent:{id:'agent',name:'Assistant',version:1,status:'active',configuration:{},hooks:[],extensions:[]},owner_id:'owner',can_execute:true});
  if(path.endsWith('/environments'))throw new Error('Metadata view tried to wake executor');
  if(path.endsWith('/environment-status'))return json({environments:[]});
  if(path.endsWith('/process-environment'))return json(view());
  if(path.endsWith('/process-environment/workspace')){
   const change=route.request().postDataJSON();writes.push(change);expect(change.set).toEqual({});expect(change.environment_id).toBeUndefined();expect(change.working_directory).toBeUndefined();expect(change.version).toBe(layer.version);
   layer={...layer,version:layer.version+1,keys:layer.keys.filter(key=>!change.remove.includes(key))};
   if(!layer.keys.length){layer.environment_id='';layer.working_directory='';}
   return json(view());
  }
  return route.fulfill({status:404,body:'{}'});
 });
 await page.goto('/t/tenant/agents/agent/settings');
 await page.getByRole('button',{name:'配置执行环境变量',exact:true}).click();
 const dialog=page.getByRole('dialog',{name:'执行环境变量'});
 await dialog.getByLabel('配置层级',{exact:true}).selectOption('workspace');
 await expect(dialog.getByLabel('变量所在执行环境',{exact:true})).toHaveValue('revoked');
 await dialog.getByRole('button',{name:'删除 ONE',exact:true}).click();
 await expect(dialog.getByRole('button',{name:'删除 ONE',exact:true})).toHaveCount(0);
 await expect(dialog.getByRole('button',{name:'删除 TWO',exact:true})).toBeEnabled();
 await dialog.getByRole('button',{name:'删除 TWO',exact:true}).click();
 await expect(dialog.getByText('本层没有覆盖，继承下层变量。',{exact:true})).toBeVisible();
 expect(writes.length).toBe(2);expect(layer.environment_id).toBe('');
});
