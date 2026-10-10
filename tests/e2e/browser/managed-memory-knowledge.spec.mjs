import {createRequire} from 'node:module';
const require=createRequire(new URL('../../../frontend/package.json',import.meta.url));
const {expect,test}=require('@playwright/test');

test('Memory domain, entity and time filters survive navigation, paging and a correction conflict',async({page},testInfo)=>{
  const user={id:'owner',email:'owner@example.test',email_verified:true};
  const source={agent_id:'agent',thread_id:'main',generation_id:'generation',fleet_id:'fleet',from:1,through:2};
  const entries=Array.from({length:21},(_,index)=>({id:`entry-${index}`,revision:3,name:`Knowledge ${index}`,summary:'Response preferences',type:'user',scope:{},body:'',sources:[source],entities:[{id:`person-${index}`,name:'Alex',kind:'person'}],facts:[{id:`fact-${index}`,domain:'preferences',subject:`person-${index}`,predicate:'prefers',value:`Preference ${index}`,status:'valid',reason:'User statement',sources:[source],source_type:'user_statement',recorded_at:'2026-09-01T00:00:00Z',valid_from:'2026-01-01T00:00:00Z'}],created_at:'2026-09-01T00:00:00Z',updated_at:'2026-09-01T00:00:00Z'}));
  const queries=[],changes=[];
  let failed=false;
  await page.route('**/api/**',async route=>{
    const url=new URL(route.request().url()),path=url.pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/fleet'))return json({owner:user,agents:[{id:'agent',name:'Assistant'}],membership:{status:'active'}});
    if(path.endsWith('/memory'))return json({enabled:true,strategy:'basic',entries:21,pending:0,epoch:1,version:1,fence:1});
    if(path.endsWith('/domains'))return json(url.searchParams.has('id')?[{id:'preferences',name:'Preferences',relations:[{predicate:'prefers',description:'Positive preference'}]}]:[{id:'preferences',name:'Preferences'},{id:'projects',name:'Projects'}]);
    if(path.endsWith('/entries'))return json({entries:[],next:0});
    if(path.endsWith('/reviews'))return json({reviews:[],next:0});
    if(path.includes('/entries/'))return json(entries.find(entry=>path.endsWith('/'+entry.id)));
    if(path.endsWith('/administer')){changes.push(route.request().postDataJSON());return route.fulfill({status:409,body:'{}'});}
    if(path.endsWith('/facts')){
      const q=Object.fromEntries(url.searchParams);queries.push(q);
      if(q.entity==='person-20'&&!failed){failed=true;return route.fulfill({status:503,body:'{}'});}
      const selected=q.domain==='projects'?[]:entries.filter(entry=>!q.entity||entry.entities[0].id===q.entity);
      const offset=Number(q.offset);
      return json({facts:selected.slice(offset,offset+20).map(entry=>({entry_id:entry.id,revision:entry.revision,scope:{},fact:{...entry.facts[0],status:q.status==='corrected'?'corrected':'valid'},subject:entry.entities[0],lifecycle:q.status||'current'})),next:offset+20<selected.length?offset+20:0,total:selected.length,domain_total:21});
    }
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/memory');
  await page.getByRole('button',{name:'领域与事实',exact:true}).click();
  await page.getByRole('button',{name:'偏好与习惯',exact:true}).click();
  await expect(page.locator('article')).toHaveCount(20);
  await page.getByRole('button',{name:'下一页',exact:true}).click();
  await expect(page.locator('article')).toHaveCount(1);
  await page.getByRole('button',{name:'Alex (person-20)',exact:true}).click();
  await page.getByRole('button',{name:'重试',exact:true}).click();
  await expect(page.getByLabel('实体 ID',{exact:false})).toHaveValue('person-20');
  await expect(page).toHaveURL(/domain=preferences.*entity=person-20/);
  await expect.poll(()=>queries.at(-1)?.offset).toBe('0');
  await page.getByRole('combobox',{name:'关系',exact:true}).selectOption('prefers');
  await page.getByRole('combobox',{name:'时间视图',exact:true}).selectOption('history');
  await page.getByRole('combobox',{name:'生命周期',exact:true}).selectOption('corrected');
  await page.getByRole('button',{name:'应用筛选',exact:true}).click();
  await expect(page.locator('article')).toContainText('已更正');
  await page.getByRole('button',{name:'审核记录',exact:true}).click();
  await page.getByRole('button',{name:'领域与事实',exact:true}).click();
  await expect(page.getByRole('combobox',{name:'关系',exact:true})).toHaveValue('prefers');
  await expect(page.getByRole('combobox',{name:'生命周期',exact:true})).toHaveValue('corrected');
  await page.reload();
  await expect(page.getByLabel('实体 ID',{exact:false})).toHaveValue('person-20');
  await page.getByRole('button',{name:'查看完整知识',exact:true}).click();
  const dialog=page.getByRole('dialog');
  await dialog.getByRole('button',{name:'更正知识',exact:true}).click();
  await dialog.getByLabel('prefers 的值',{exact:true}).fill('New preference kept on conflict');
  await dialog.getByRole('button',{name:'提交更正',exact:true}).click();
  await expect(dialog.getByRole('alert')).toBeVisible();
  await expect(dialog.getByLabel('prefers 的值',{exact:true})).toHaveValue('New preference kept on conflict');
  expect(changes).toHaveLength(1);
  expect(changes[0].changes[0].expected_revision).toBe(3);
  await dialog.getByRole('button',{name:'关闭',exact:true}).click();
  await expect(page.getByLabel('实体 ID',{exact:false})).toHaveValue('person-20');
  await page.getByRole('combobox',{name:'时间视图',exact:true}).selectOption('as_of');
  await page.getByLabel('有效时间点（本地时间）',{exact:true}).fill('2026-06-01T09:00');
  await page.getByRole('button',{name:'应用筛选',exact:true}).click();
  await expect.poll(()=>queries.at(-1)?.view).toBe('as_of');
  expect(queries.at(-1).at).toMatch(/^2026-06-01T\d{2}:00:00\.000Z$/);
  expect(queries.at(-1).status).toBe('');
  await expect(page.getByText('按所选时刻的有效期查看。',{exact:false})).toBeVisible();
  await expect(page.getByRole('link',{name:'来源对话 1',exact:true})).toHaveAttribute('href','/t/tenant/agents/agent?thread=main');
  await page.getByRole('button',{name:'项目与职业',exact:true}).click();
  await expect(page.getByRole('heading',{name:'暂无匹配的事实'})).toBeVisible();
  await expect(page.getByLabel('实体 ID',{exact:false})).toHaveValue('');
  await page.setViewportSize({width:390,height:844});
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('memory-facts-mobile.png'),fullPage:true});
});

test('disabled Memory facts remain readable without an Agent list for delegated owners',async({page})=>{
  const user={id:'admin',email:'admin@example.test',email_verified:true};
  const fact={id:'f',domain:'preferences',subject:'person',predicate:'prefers',value:'Retained',status:'valid',reason:'User said so',sources:[],recorded_at:'2026-01-01T00:00:00Z'};
  await page.route('**/api/**',route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'admin'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/fleet'))return route.fulfill({status:503,body:'{}'});
    if(path.endsWith('/memory'))return json({enabled:false,strategy:'basic',entries:1,pending:0});
    if(path.endsWith('/domains'))return json([]);
    if(path.endsWith('/facts'))return json({facts:[{entry_id:'entry',revision:1,fact,subject:{id:'person',name:'Alex'},scope:{},lifecycle:'current'}],next:0,total:1,domain_total:1});
    if(path.endsWith('/entries/entry'))return json({id:'entry',name:'Retained knowledge',revision:1,body:'Retained body',scope:{},sources:[]});
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/users/first/memory?tab=facts');
  await expect(page.locator('article')).toContainText('Retained');
  await page.getByRole('button',{name:'查看完整知识',exact:true}).click();
  await expect(page.getByRole('button',{name:'更正知识',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'遗忘',exact:true})).toBeDisabled();
  await page.getByRole('button',{name:'关闭',exact:true}).click();
  await page.goto('/t/tenant/users/second/memory?tab=facts');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.locator('article')).toContainText('Retained');
});
