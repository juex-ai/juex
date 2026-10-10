import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('Workspace reads explicit locations, retries receipts, previews and preserves WorkingFiles origin', async ({ page }, testInfo) => {
  const user = { id:'owner', email:'owner@example.test', email_verified:true };
  const agent = { id:'agent', name:'Assistant', status:'active', worker_depth:1 };
  const thread = { id:'main', agent_id:'agent', kind:'main', name:'Main', retention:'active', state:'idle', generation:1, sequence:0, pending_inputs:0, held_inputs:0 };
  const environments = [{ id:'host',name:'Mac Studio',kind:'host',online:true,default:true,working_directory:'/workspace',capabilities:['files'],authorization_version:3 }, { id:'original',name:'Original Linux',kind:'host',online:false,default:false,working_directory:'/different',capabilities:['files'],authorization_version:2 }];
  const bytes=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg==','base64');
  const sha=createHash('sha256').update(bytes).digest('hex');
  const file=(name)=>({path:name,name,kind:'file',size:bytes.length,modified_at:'2026-10-10T01:00:00Z'});
  const requests=[], transfers=[]; const receipts=new Map(); let failInitial=true, failImage=true;
  await page.route('**/api/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    const json=value=>route.fulfill({contentType:'application/json',body:JSON.stringify(value)});
    if(path==='/api/auth/session')return json(user);
    if(path==='/api/config')return json({email_enabled:false});
    if(path==='/api/tenants')return json([{id:'tenant',name:'Workspace',role:'member'}]);
    if(path.endsWith('/notifications'))return json({items:[],unread:0});
    if(path.endsWith('/agents/agent'))return json({agent,owner_id:user.id,can_execute:true});
    if(path.endsWith('/threads'))return json([thread]);
    if(path.endsWith('/events'))return json({thread,events:[],next_sequence:0});
    if(path.endsWith('/environments'))return json(environments);
    if(path.endsWith('/inspection'))return json({thread,working_files:{environment_id:'original',directory:'/retained/thread-main'}});
    if(path.endsWith('/artifacts'))return json([]);
    if(path.endsWith('/workspace-reads') && route.request().method()==='POST'){
      const request=route.request().postDataJSON(); requests.push(request);
      const entry=file(request.query.path);
      const preview=request.query.read?{entry,text:request.query.path==='note.txt'?'中文 preview\nline two':request.query.path==='code.go'?'package main\nconst title = "<script>no execution</script>"':'',binary:!['note.txt','code.go'].includes(request.query.path),media_type:request.query.path==='image.png'?'image/png':'text/plain',truncated:false}:undefined;
      const entries=request.query.search?[file('nested/match.txt')]:[file('note.txt'),file('code.go'),file('image.png'),...(request.query.hidden?[file('.hidden')]:[])];
      const receipt={operation_id:request.request_id,environment_id:request.environment_id,directory:request.directory,query:request.query,state:'waiting',listing:{entries,next_cursor:'',scan_limited:false,preview}};
      receipts.set(request.request_id,receipt);
      if(failInitial){failInitial=false;return route.fulfill({status:503,body:'{}'});}
      return json({...receipt,listing:undefined});
    }
    if(path.includes('/workspace-reads/'))return json({...receipts.get(path.split('/').at(-1)),state:'completed'});
    if(path.endsWith('/transfers')){transfers.push(route.request().postDataJSON());return json({id:'transfer',state:'completed',artifact_id:'snapshot'});}
    if(path.endsWith('/artifacts/snapshot'))return json({id:'snapshot',state:'ready',request:{name:'image.png',manifest:{size:bytes.length,sha256:sha}}});
    if(path.endsWith('/artifacts/snapshot/download')){if(failImage){failImage=false;return route.fulfill({status:503,body:'{}'});}return route.fulfill({body:bytes,contentType:'image/png'});}
    return route.fulfill({status:404,body:'{}'});
  });
  await page.goto('/t/tenant/agents/agent');
  await page.getByRole('button',{name:'文件与产物'}).click();
  await expect(page.getByRole('button',{name:'重试',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'重试',exact:true}).click();
  await expect(page.getByRole('button',{name:'note.txt',exact:true})).toBeVisible();
  expect(requests[0].request_id).toBe(requests[1].request_id);
  await page.getByLabel('显示隐藏文件').check();
  await expect(page.getByRole('button',{name:'.hidden',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'note.txt',exact:true}).click();
  await expect(page.getByRole('region',{name:'文件预览'})).toContainText('中文 preview');
  await page.getByRole('button',{name:'复制内容',exact:true}).click();
  await expect(page.getByRole('button',{name:'已复制内容'})).toBeVisible();
  await page.getByRole('button',{name:'关闭预览'}).click();
  await page.getByRole('button',{name:'code.go',exact:true}).click();
  const code=page.getByLabel('文件内容',{exact:true});
  await expect(code).toHaveAttribute('data-language','go');
  await expect(code.locator('[data-line]')).toHaveCount(2);
  await expect(code.locator('.management-file-token').first()).toBeVisible();
  await expect(code).toContainText('<script>no execution</script>');
  await expect(code.locator('script')).toHaveCount(0);
  await expect(code).toHaveCSS('white-space','pre');
  await page.getByRole('button',{name:'自动换行',exact:true}).click();
  await expect(code).toHaveCSS('white-space','pre-wrap');
  await page.getByRole('button',{name:'关闭预览'}).click();
  await page.getByRole('button',{name:'image.png',exact:true}).click();
  await page.getByRole('button',{name:'查看图片与下载'}).click();
  await expect(page.getByRole('button',{name:'重试读取文件'})).toBeVisible();
  await page.getByRole('button',{name:'重试读取文件'}).click();
  await expect(page.getByRole('img',{name:'image.png'})).toBeVisible();
  await expect(page.getByRole('link',{name:'下载原文件'})).toHaveAttribute('download','image.png');
  expect(transfers[0]).toEqual(transfers[1]);
  expect(transfers[0].source).toEqual({environment_id:'host',authorization_version:3,path:'image.png',working_directory:'/workspace'});
  await page.getByLabel('搜索文件名').fill('match');
  await page.getByRole('button',{name:'搜索',exact:true}).click();
  await expect(page.getByRole('button',{name:'nested/match.txt',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'对话工作文件'}).click();
  await expect(page.getByText('此环境当前离线。读取会等待原环境上线，不会切换到其它环境。')).toBeVisible();
  await expect.poll(()=>requests.at(-1)?.directory).toBe('/retained/thread-main');
  expect(requests.at(-1).environment_id).toBe('original');
  await page.setViewportSize({width:390,height:844});
  const sheet=page.getByRole('dialog',{name:'文件与产物',exact:true});
  await expect(sheet).toHaveAttribute('data-slot','sheet-content');
  await expect.poll(async()=>Math.round((await sheet.boundingBox()).width)).toBe(390);
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('workspace-mobile.png'),fullPage:true});
});
