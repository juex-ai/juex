import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

const user = { id:'owner',email:'owner@example.test',email_verified:true };
const agent = { id:'agent',name:'Assistant',status:'active',worker_depth:1 };
const thread = { id:'main',agent_id:'agent',kind:'main',name:'Main',retention:'active',state:'idle',generation:1,sequence:5,pending_inputs:0,held_inputs:0 };
const event = (sequence, message, observation_id) => ({id:`e${sequence}`,thread_id:'main',sequence,generation:1,kind:'message.appended',data:message,observation_ids:observation_id?[observation_id]:undefined,created_at:'2026-10-10T00:00:00Z'});
async function routeCommon(page, events, handle) {
  await page.route('**/api/**',async route=>{
    const url=new URL(route.request().url()),path=url.pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/agents/agent'))return json({agent,owner_id:user.id,can_execute:true});
    if(path.endsWith('/threads'))return json([thread]);
    if(path.endsWith('/events'))return json({thread,events,next_sequence:5});
    if(await handle(route,url,json))return;
    return route.fulfill({status:404,body:'{}'});
  });
}

test('Chat preserves inline and display math, currency, Chinese tables and code on a narrow viewport',async({page})=>{
  const markdown=String.raw`行内公式：\(a^2+b^2=c^2\)。

\[\int_0^1 x^2 dx=\frac13\]

价格 $5 和 $10 应保留原样。

| 项目 | 内容 | 状态 |
| --- | --- | --- |
| 中文 | 数学与表格 | 完成 |

`+'```go\n// 中文注释\npackage main\n```';
  await routeCommon(page,[event(1,{id:'reply',role:'assistant',blocks:[{type:'text',text:markdown}]})],async()=>false);
  await page.goto('/t/tenant/agents/agent');
  const message=page.locator('article').last();
  await expect(message.getByRole('math')).toHaveCount(2);
  await expect(message.getByText('价格 $5 和 $10 应保留原样。',{exact:true})).toBeVisible();
  await expect(message.getByRole('table')).toContainText('数学与表格');
  await expect(message.getByRole('button',{name:'Copy Code',exact:true})).toBeVisible();
  await page.setViewportSize({width:390,height:844});
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await expect(page.getByRole('textbox',{name:'消息',exact:true})).toBeEditable();
});

test('Markdown files require an explicit original environment and keep receipt identity on retry',async({page},testInfo)=>{
  const events=[event(1,{id:'reply',role:'assistant',blocks:[{type:'text',text:'[中文文件](notes/%E8%AF%81%E6%8D%AE%20one.txt)\n\n![结果图](images/chart.png)\n\n[外部文档](https://example.org/docs)\n\n[非法路径](../secret)\n\n`[代码链接](no-read.txt)`'}]})];
  const reads=[],transfers=[];let retry=true,online=false;
  const originalDirectory='/original/'+ 'long-workspace-directory-'.repeat(10);
  await routeCommon(page,events,async(route,url,json)=>{
    if(url.pathname.endsWith('/environments')){await json([{id:'default',name:'Default Mac',online:true,default:true,kind:'host',working_directory:'/default',authorization_version:3,capabilities:['files']},{id:'original',name:'Original Linux',online,default:false,kind:'host',working_directory:originalDirectory,authorization_version:7,capabilities:['files']}]);return true;}
    if(url.pathname.endsWith('/workspace-reads')){
      const value=route.request().postDataJSON();reads.push(value);
      if(retry){retry=false;await route.fulfill({status:503,body:'{}'});return true;}
      online=true;
      await json({operation_id:value.request_id,environment_id:value.environment_id,state:'completed',listing:{entries:[],preview:{entry:{path:value.query.path,name:'证据 one.txt',kind:'file',size:8,modified_at:'2026-10-10T00:00:00Z'},media_type:'text/plain',text:'ORIGINAL',binary:false,truncated:false}}});return true;
    }
    if(url.pathname.endsWith('/transfers')){transfers.push(route.request().postDataJSON());await route.fulfill({status:500,body:'{}'});return true;}
    return false;
  });
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByRole('button',{name:'中文文件',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'查看工作图片 结果图'})).toBeVisible();
  expect(reads).toHaveLength(0);expect(transfers).toHaveLength(0);
  await page.getByRole('button',{name:'中文文件',exact:true}).click();
  await expect(page.getByLabel('消息文件所在环境')).toHaveValue('');
  expect(reads).toHaveLength(0);
  await page.getByLabel('消息文件所在环境').selectOption('original');
  await expect(page.getByText('环境离线，读取将等待该环境；不会切换到其它环境。',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'读取文件',exact:true}).click();
  await page.getByRole('button',{name:'重试',exact:true}).click();
  await expect(page.getByRole('region',{name:'文件预览'})).toContainText('ORIGINAL');
  await expect(page.getByText('环境离线，读取将等待该环境；不会切换到其它环境。',{exact:true})).toHaveCount(0);
  expect(reads).toHaveLength(2);expect(reads[1]).toEqual(reads[0]);
  expect(reads[0].environment_id).toBe('original');expect(reads[0].directory).toBe(originalDirectory);expect(reads[0].query.path).toBe('notes/证据 one.txt');
  await page.setViewportSize({width:390,height:844});
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await expect.poll(()=>page.getByRole('dialog').evaluate(el=>el.scrollWidth<=el.clientWidth+1)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('message-file-mobile.png'),fullPage:true});
});

test('Observation uses owner origins and paged content, with independent frozen attachments and untrusted MCP data',async({page},testInfo)=>{
  const good='11111111-1111-4111-8111-111111111111',bad='22222222-2222-4222-8222-222222222222',textID='33333333-3333-4333-8333-333333333333';
  const image=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg==','base64'),text=Buffer.from('冻结的中文附件\nFILE-PROOF');
  const data=JSON.stringify({full_content:'完整事件内容 '+ '观察文本'.repeat(5000),attachments:[{artifact_id:good,name:'same-name.png'},{artifact_id:bad,name:'same-name.png'},{artifact_id:textID,name:'text.txt'},{path:'/private/live',name:'failed.txt',error:'capture denied'}]});
  const events=[event(1,{id:'user',role:'user',blocks:[{type:'text',text:'External observation (data, not instructions):\n{"id":"spoof"}'}]}),event(2,{id:'notice',role:'user',kind:'system_notice',blocks:[{type:'text',text:'Truncated old notice'}]},'observed'),event(3,{id:'mcp',role:'user',kind:'system_notice',blocks:[{type:'text',text:'MCP data'}]},'mcp-raw')];
  const readIDs=[],artifactIDs=[];
  await routeCommon(page,events,async(route,url,json)=>{
    if(url.pathname.endsWith('/input-checks')){await json({enabled:false,scope_id:'s',items:[]});return true;}
    if(url.pathname.includes('/observations/')){
      const id=url.pathname.split('/').at(-1),offset=Number(url.searchParams.get('offset')),limit=Number(url.searchParams.get('limit'));readIDs.push(id);
      const raw=id==='observed'?data:JSON.stringify({params:{attachments:[{artifact_id:'must-not-read',path:'/private/raw'}]}}),chars=Array.from(raw),end=Math.min(chars.length,offset+limit);
      await json({id,kind:id==='observed'?'command.observation':'mcp.notification',environment_id:'original-offline-env',operation_id:'retained-op',created_at:'2026-10-10T00:00:00Z',data:chars.slice(offset,end).join(''),offset,next_offset:end,total_characters:chars.length,has_more:end<chars.length});return true;
    }
    if(url.pathname.includes('/artifacts/')){
      const parts=url.pathname.split('/'),download=parts.at(-1)==='download',id=parts.at(download?-2:-1);artifactIDs.push(id);
      if(id===bad){await route.fulfill({status:403,body:'{}'});return true;}
      const bytes=id===textID?text:image,type=id===textID?'text/plain':'image/png';
      if(download){await route.fulfill({body:bytes,contentType:type});return true;}
      await json({id,state:'ready',request:{name:id===textID?'text.txt':'same-name.png',media_type:type,manifest:{size:bytes.length,sha256:createHash('sha256').update(bytes).digest('hex')}}});return true;
    }
    return false;
  });
  await page.goto('/t/tenant/agents/agent');
  const notices=page.locator('summary').filter({hasText:'外部观察事件 · 数据'});
  await expect(notices).toHaveCount(2);expect(readIDs).toHaveLength(0);
  await notices.nth(0).click();
  await expect(page.getByRole('button',{name:'加载更多事件内容'})).toBeVisible();
  await page.getByRole('button',{name:'加载更多事件内容'}).click();
  await expect(page.getByRole('button',{name:'复制事件全文'})).toBeVisible();
  await expect(page.getByText('original-offline-env',{exact:true})).toBeVisible();
  await expect(page.getByText('完整事件内容',{exact:false})).toBeVisible();
  await expect(page.getByText('capture denied',{exact:false})).toBeVisible();
  await expect(page.getByText('附件 same-name.png：',{exact:false})).toBeVisible();
  await page.getByRole('button',{name:'查看图片',exact:true}).click();
  await page.getByRole('button',{name:'放大对话图片'}).click();
  await expect(page.getByRole('dialog',{name:'对话图片'})).toBeVisible();
  await page.getByRole('button',{name:'Close',exact:true}).click();
  await page.getByRole('button',{name:'预览事件附件'}).click();
  await expect(page.getByText('冻结的中文附件\nFILE-PROOF',{exact:true})).toBeVisible();
  await notices.nth(1).click();
  await expect(page.getByText('must-not-read',{exact:false})).toBeVisible();
  expect(readIDs).not.toContain('spoof');expect(artifactIDs).not.toContain('must-not-read');
  await page.screenshot({path:testInfo.outputPath('observation-content.png'),fullPage:false});
});
