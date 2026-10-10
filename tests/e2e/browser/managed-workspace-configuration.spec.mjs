import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

test('Workspace configuration previews its source, retries the same read and reconciles a lost apply response',async({page})=>{
  const user={id:'owner',email:'owner@example.test',email_verified:true};
  let agent={id:'agent',name:'Assistant',status:'active',version:1,configuration:{},instructions:'',worker_depth:1,hooks:[],extensions:[]};
  const configuration={version:1,declaration:{models:['model'],modules:{mcp:false}},environment_id:'device',working_directory:'/device/work',path:'.juex/juex.yaml',operation_id:'operation',authorization_version:2,read_started_at:'2026-10-10T01:00:00Z',sha256:'a'.repeat(64)};
  const effective={models:['model'],model_source:{layer:'workspace',version:1},modules:{mcp:{enabled:false,source:{layer:'workspace',version:1}}}};
  const reads=[],writes=[];
  let previewFailed=false;
  const receipt={operation_id:'operation',environment_id:'device',directory:'/device/work',state:'completed',authorization_version:2,read_started_at:configuration.read_started_at,query:{path:configuration.path,read:true,hidden:true},listing:{entries:[],preview:{entry:{path:configuration.path,kind:'file',size:18},text:'modules: {mcp: false}'}}};
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/agents/agent'))return json({agent,owner_id:user.id,can_execute:true,layers:{tenant:{version:1,declaration:{models:['model']}},fleet:{version:1,declaration:{}},workspace:agent.workspace_configuration??{version:0,declaration:{}},agent:{version:agent.version,declaration:agent.configuration}},effective:agent.workspace_configuration?effective:{models:['model'],model_source:{layer:'tenant',version:1},modules:{mcp:{enabled:true,source:{layer:'default',version:0}}}}});
    if(path.endsWith('/models'))return json([{id:'model',provider:'fixture',name:'primary'}]);
    if(path.endsWith('/environments'))return json([{id:'device',name:'Selected Mac',kind:'device',default:true,online:true,capabilities:['files'],working_directory:'/device/work',authorization_version:2}]);
    if(path.endsWith('/workspace-reads')){reads.push(route.request().postDataJSON());return json(receipt);}
    if(path.includes('/workspace-configurations/')){
      if(!previewFailed){previewFailed=true;return route.fulfill({status:503,body:'{}'});}
      return json({receipt:{...receipt,listing:undefined},configuration,effective});
    }
    if(path.endsWith('/workspace-configuration')){
      const change=route.request().postDataJSON();writes.push(change);expect(change.version).toBe(agent.version);
      agent={...agent,version:agent.version+1,workspace_configuration:change.remove?undefined:configuration};
      if(!change.remove)return route.fulfill({status:503,body:'{}'});
      return json(agent);
    }
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent/settings');
  await page.getByLabel('专属指令').fill('Keep this unsaved draft');
  await expect(page.getByText('当前生效 · Tenant · v1',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'导入 Workspace 配置',exact:true}).click();
  const dialog=page.getByRole('dialog');
  await expect(dialog.getByLabel('配置 Workspace 目录')).toHaveValue('/device/work');
  await expect(dialog.getByLabel('配置文件相对路径')).toHaveValue('.juex/juex.yaml');
  await dialog.getByRole('button',{name:'读取并预览配置',exact:true}).click();
  await dialog.getByRole('button',{name:'重试',exact:true}).click();
  await expect(dialog.getByLabel('Workspace 配置预览')).toContainText('来自 Selected Mac：/device/work/.juex/juex.yaml');
  await expect(dialog.getByLabel('Workspace 配置预览')).toContainText('应用后预期 · Workspace 快照 · v1');
  expect(new Set(reads.map(value=>value.request_id)).size).toBe(1);
  expect(writes).toHaveLength(0);
  await dialog.getByRole('button',{name:'应用此配置快照',exact:true}).click();
  await expect(dialog.getByText('Workspace 配置快照已应用。文件后续修改不会自动生效。',{exact:true})).toBeVisible();
  expect(writes).toHaveLength(1);
  expect(writes[0].sha256).toBe(configuration.sha256);
  await expect(dialog.getByText('当前已应用 · v1',{exact:true})).toBeVisible();
  await dialog.getByRole('button',{name:'Close',exact:true}).click();
  await expect(page.getByText('当前生效 · Workspace 快照 · v1',{exact:true})).toBeVisible();
  await expect(page.getByText('当前关闭 · Workspace 快照 · v1',{exact:true})).toBeVisible();
  await expect(page.getByLabel('专属指令')).toHaveValue('Keep this unsaved draft');
  await page.getByRole('button',{name:'导入 Workspace 配置',exact:true}).click();
  await dialog.getByRole('button',{name:'移除快照并恢复继承',exact:true}).click();
  await expect(dialog.getByText('Workspace 快照已移除，恢复下层配置。',{exact:true})).toBeVisible();
  expect(writes).toHaveLength(2);
  expect(writes[1].remove).toBe(true);
  await dialog.getByRole('button',{name:'Close',exact:true}).click();
  await expect(page.getByText('当前生效 · Tenant · v1',{exact:true})).toBeVisible();
  await expect(page.getByText('当前开启 · 产品默认',{exact:true})).toBeVisible();
  await expect(page.getByLabel('专属指令')).toHaveValue('Keep this unsaved draft');
});
