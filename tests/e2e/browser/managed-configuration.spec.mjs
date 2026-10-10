import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

const catalog=['a','b','c'].map(id=>({id,provider:'fixture',name:`model-${id}`,enabled:true}));

test('Agent model order, module inheritance, source layers and conflicts remain explicit',async({page})=>{
  const user={id:'owner',email:'owner@example.test',email_verified:true};
  const tenant={id:'tenant',name:'Workspace',role:'admin'};
  let agent={id:'agent',name:'Assistant',status:'active',version:1,worker_depth:1,instructions:'Original',configuration:{models:['a','c'],modules:{memory:false}},hooks:[],extensions:[]};
  let conflict=false;
  const writes=[];
  const detail=()=>({agent,owner_id:user.id,can_execute:true,layers:{tenant:{version:2,declaration:{models:['b']}},fleet:{version:3,declaration:{models:['c','b'],modules:{mcp:false}}},workspace:{version:1,declaration:{models:['a','b']}},agent:{version:agent.version,declaration:agent.configuration}},effective:{models:agent.configuration.models??['a','b'],model_source:{layer:agent.configuration.models?'agent':'workspace',version:agent.configuration.models?agent.version:1},modules:{mcp:{enabled:agent.configuration.modules?.mcp??false,source:{layer:agent.configuration.modules?.mcp==null?'fleet':'agent',version:3}}}}});
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([tenant]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/models'))return json(catalog);
    if(path.endsWith('/agents/agent')){
      if(route.request().method()==='PUT'){
        const change=route.request().postDataJSON();writes.push(change);
        if(conflict){conflict=false;agent={...agent,version:agent.version+1,instructions:'Other editor'};return route.fulfill({status:409,body:'{}'});}
        expect(change.version).toBe(agent.version);
        agent={...agent,...change,version:agent.version+1};return json(agent);
      }
      return json(detail());
    }
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent/settings');
  await expect(page.getByLabel('第 1 优先模型')).toHaveValue('a');
  await expect(page.getByLabel('MCP 工具配置')).toHaveValue('inherit');
  await expect(page.getByText('当前关闭 · Fleet · v3',{exact:true})).toBeVisible();
  await page.getByText('查看各层已保存配置与覆盖关系',{exact:true}).click();
  await expect(page.getByRole('table')).toContainText('fixture / model-c → fixture / model-b');
  await page.getByRole('button',{name:'上移第 2 个模型',exact:true}).click();
  await page.getByLabel('MCP 工具配置').selectOption('on');
  await page.getByLabel('多文件补丁编辑配置').selectOption('on');
  await page.getByLabel('分块缓冲写入配置').selectOption('on');
  await page.getByLabel('模块预设',{exact:true}).selectOption('minimal');
  await page.getByRole('button',{name:'保存',exact:true}).click();
  await expect(page.getByText('Agent 配置已保存。',{exact:true})).toBeVisible();
  expect(writes[0].configuration).toEqual({models:['c','a'],module_preset:'minimal',modules:{memory:false,mcp:true,'apply-patch':true,'chunked-write':true}});
  await page.getByLabel('继承下层模型顺序').check();
  await page.getByLabel('MCP 工具配置').selectOption('inherit');
  await page.getByLabel('多文件补丁编辑配置').selectOption('inherit');
  await page.getByLabel('分块缓冲写入配置').selectOption('inherit');
  await page.getByLabel('模块预设',{exact:true}).selectOption('');
  await page.getByRole('button',{name:'保存',exact:true}).click();
  await expect.poll(()=>writes.length).toBe(2);
  expect(writes[1].configuration).toEqual({modules:{memory:false}});
  await expect(page.getByText('当前生效 · Workspace 快照 · v1',{exact:true})).toBeVisible();
  await page.getByLabel('专属指令').fill('My draft');conflict=true;
  await page.getByRole('button',{name:'保存',exact:true}).click();
  await expect(page.getByText('服务器上的 Agent 设置已改变，你的编辑仍然保留。',{exact:false})).toBeVisible();
  await expect(page.getByLabel('专属指令')).toHaveValue('My draft');
  await page.getByRole('button',{name:'丢弃本地编辑并加载服务器设置',exact:true}).click();
  await expect(page.getByLabel('专属指令')).toHaveValue('Other editor');
  expect(writes).toHaveLength(3);
});

test('Tenant defaults keep a conflicting draft until explicitly reloaded',async({page})=>{
  let settings={version:1,declaration:{models:['a'],modules:{mcp:false}}};
  let writes=0;
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json({id:'owner',email:'owner@example.test',email_verified:true});
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/models'))return json(catalog);
    if(path.endsWith('/settings')){
      if(route.request().method()==='PUT'){writes++;settings={version:2,declaration:{models:['b'],modules:{mcp:true}}};return route.fulfill({status:409,body:'{}'});}
      return json(settings);
    }
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/settings');
  await page.getByLabel('第 1 优先模型').selectOption('c');
  await page.getByRole('button',{name:'保存 Tenant 配置',exact:true}).click();
  await expect(page.getByText('服务器配置已改变，本地编辑仍保留。',{exact:false})).toBeVisible();
  await expect(page.getByLabel('第 1 优先模型')).toHaveValue('c');
  await page.getByRole('button',{name:'加载服务器配置',exact:true}).click();
  await expect(page.getByLabel('第 1 优先模型')).toHaveValue('b');
  await expect(page.getByLabel('MCP 工具配置')).toHaveValue('on');
  expect(writes).toBe(1);
});
