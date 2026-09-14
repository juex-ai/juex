import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');
const file = path => ({name:path.split('/').at(-1),path,is_dir:false});
const baseline = {name:'workspace',path:'/',is_dir:true,children:[
  {name:'src',path:'src',is_dir:true,children:[file('src/index.html'),file('src/config.json')]},
  {name:'.cache',path:'.cache',is_dir:true,children:[file('.cache/hidden.txt')]},file('.env'),file('README.md'),file('empty.txt'),file('binary.dat')
]};
const source = '<!DOCTYPE html>\n<script>window.previewExecuted = true</script>\n' + 'x'.repeat(160);
async function fixture(page, options = {}) {
  await page.addInitScript(() => {
    window.sources = [];
    window.copiedText = '';
    Object.defineProperty(navigator, 'clipboard', {configurable:true,value:{writeText:async text => {window.copiedText=text;}}});
    window.EventSource = class extends EventTarget {
      static OPEN=1; static CLOSED=2; static CONNECTING=0;
      constructor(url){super();this.url=String(url);window.sources.push(this);}
      readyState=1; close(){this.readyState=2;}
    };
  });
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const json = (body,status=200) => route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
    if (path === '/api/agents') return json([{id:'files-agent',name:'Files agent',workspace:'/tmp/workspace',enabled:true,binding:'bound',runtime_health:'healthy',runtime_present:true,activity:{state:'idle',pending_input_count:0}}]);
    if(path.endsWith('/threads')) return json({active_threads:[],archived_threads:[]});
    if(path.endsWith('/files/tree')) return json(options.tree ?? baseline);
    if(path.endsWith('/files/content')) {
      if(options.content) return options.content(route, url.searchParams.get('path'));
      const name=url.searchParams.get('path');
      if(name === 'binary.dat') return json({error:{message:'binary file preview is not supported'}},415);
      return json({path:name,content:name==='empty.txt'?'':source,kind:'text',size:source.length,truncated:false});
    }
    if(path.endsWith('/files/raw')) return route.fulfill({headers:{'Content-Type':'application/octet-stream','Content-Disposition':'attachment; filename="index.html"'},body:source+'original tail'});
    return route.fulfill({status:404,body:'not found'});
  });
  await page.setViewportSize({width:options.mobile?390:1440,height:900});
  await page.goto('/agents/files-agent/threads');
  if(options.mobile) await page.getByRole('button',{name:'Open sidebar',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'Find files'})).toBeVisible();
}
async function search(page, text) {await page.getByRole('textbox',{name:'Find files'}).fill(text);}
const preview = page => page.getByRole('dialog').filter({has:page.getByRole('region',{name:'File content'})});
async function changed(page) {
  await page.evaluate(() => window.sources.find(s=>s.url.endsWith('/resource-events') && s.readyState===1)
    .dispatchEvent(new MessageEvent('message',{data:JSON.stringify({type:'resource.changed',resources:['workspace']})})));
}

test('finds collapsed paths, hides dotfiles and retains expanded folders after clearing search', async ({page}) => {
  await fixture(page);
  await expect(page.getByLabel('File root location')).toHaveText('/tmp/workspace');
  await expect(page.getByRole('button',{name:'.cache',exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'src',exact:true}).click();
  await search(page,'INDEX.HTML');
  await expect(page.getByRole('button',{name:'src/index.html',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'Clear file search'}).click();
  await expect(page.getByRole('button',{name:'src',exact:true})).toHaveAttribute('aria-expanded','true');
  await search(page,'hidden');
  await expect(page.getByText('No matching files.',{exact:true})).toBeVisible();
  await page.getByRole('checkbox',{name:'Show hidden files'}).check();
  await expect(page.getByRole('button',{name:'.cache/hidden.txt',exact:true})).toBeVisible();
});
test.describe('file search in a Turkish browser locale', () => {
  test.use({locale:'tr-TR'});
  test('matches ASCII filenames in either case', async ({page}) => {
    await fixture(page,{tree:{...baseline,children:[file('INDEX.HTML'),file('index.ts')]}});
    expect(await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().locale)).toBe('tr-TR');
    await search(page,'index.html');
    await expect(page.getByRole('button',{name:'INDEX.HTML',exact:true})).toBeVisible();
    await search(page,'INDEX.TS');
    await expect(page.getByRole('button',{name:'index.ts',exact:true})).toBeVisible();
  });
});
for (const mobile of [false,true]) test(`source preview copies, wraps, downloads and returns focus (${mobile?'phone':'desktop'})`, async ({page}) => {
  await fixture(page,{mobile});
  await search(page,'index.html');
  const trigger=page.getByRole('button',{name:'src/index.html',exact:true});
  await trigger.click();
  await expect(preview(page).locator('pre[data-language="html"]')).toBeVisible();
  await expect(preview(page).locator('[data-line="2"] span[style]').first()).toBeVisible();
  expect(await page.evaluate(()=>window.previewExecuted)).toBeUndefined();
  const code = preview(page).locator('pre');
  await expect(code).toHaveCSS('white-space','pre');
  await page.getByRole('button',{name:'Wrap lines',exact:true}).click();
  await expect(code).toHaveCSS('white-space','pre-wrap');
  await page.getByRole('button',{name:'Copy content',exact:true}).click();
  await expect.poll(()=>page.evaluate(()=>window.copiedText)).toBe(source);
  const [download] = await Promise.all([page.waitForEvent('download'),page.getByRole('link',{name:'Download original'}).click()]);
  expect(download.suggestedFilename()).toBe('index.html');
  expect(download.url()).toContain('/agents/files-agent/api/files/raw?path=src%2Findex.html&download=1');
  await page.keyboard.press('Escape');
  await expect(trigger).toBeFocused();
  await expect(page.getByRole('textbox',{name:'Find files'})).toHaveValue('index.html');
  if(mobile){await page.getByRole('button',{name:'Close sidebar',exact:true}).click();await expect(page.getByRole('button',{name:'Open sidebar',exact:true})).toBeFocused();}
});

test('loading can be cancelled and late content cannot reopen the preview', async ({page}) => {
  let release;
  const held=new Promise(resolve=>{release=resolve;});
  await fixture(page,{content:async(route,path)=>{await held;await route.fulfill({contentType:'application/json',body:JSON.stringify({path,content:'late file',size:9,truncated:false})});}});
  await page.getByRole('button',{name:'README.md',exact:true}).click();
  await expect(page.getByText('Loading file…')).toBeVisible();
  await page.keyboard.press('Escape');
  release();
  await expect(preview(page)).toHaveCount(0);
  await expect(page.getByRole('button',{name:'README.md',exact:true})).toBeFocused();
});

test('failed previews support retry and do not offer error text as file content', async ({page}) => {
  let attempts=0;
  await fixture(page,{content:(route,path)=>route.fulfill({status:++attempts===1?500:200,contentType:'application/json',body:JSON.stringify(attempts===1?{error:{message:'Read failed'}}:{path,content:'retried content',size:15,truncated:false})})});
  await page.getByRole('button',{name:'README.md',exact:true}).click();
  await expect(preview(page).getByRole('alert')).toContainText('Preview unavailable');
  await expect(page.getByRole('button',{name:'Copy content',exact:true})).toHaveCount(0);
  await expect(page.getByRole('link',{name:'Download original'})).toBeVisible();
  await page.getByRole('button',{name:'Retry preview'}).click();
  await expect(preview(page).getByRole('region',{name:'File content'})).toContainText('retried content');
});

test('refresh cannot reopen a closed preview or replace another selection', async ({page}) => {
  let release;
  let started;
  const refreshed=new Promise(resolve=>{started=resolve;});
  const held=new Promise(resolve=>{release=resolve;});
  let reads=0;
  await fixture(page,{content:async(route,path)=>{
    if(++reads===2){started();await held;}
    await route.fulfill({contentType:'application/json',body:JSON.stringify({path,content:path,size:path.length,truncated:false})});
  }});
  await page.getByRole('button',{name:'README.md',exact:true}).click();
  await expect(page.getByRole('button',{name:'Copy content'})).toBeVisible();
  await changed(page);
  await refreshed;
  await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'empty.txt',exact:true}).click();
  await expect(preview(page).getByRole('heading')).toHaveText('empty.txt');
  release();
  await expect(preview(page).getByRole('region',{name:'File content'})).toContainText('empty.txt');
  await expect(preview(page).getByRole('heading')).toHaveText('empty.txt');
});

test('truncated files have explicit preview copy and original download actions', async ({page}) => {
  await fixture(page,{content:(route,path)=>route.fulfill({contentType:'application/json',body:JSON.stringify({path,content:'partial',size:300000,truncated:true})})});
  await page.getByRole('button',{name:'README.md',exact:true}).click();
  await expect(page.getByText(/Showing the first 256 KB/)).toBeVisible();
  await page.getByRole('button',{name:'Copy preview',exact:true}).click();
  await expect.poll(()=>page.evaluate(()=>window.copiedText)).toBe('partial');
  await expect(page.getByRole('link',{name:'Download original'})).toHaveAttribute('href',/download=1$/);
});

test('empty and hidden-only roots keep the full root path',async({page})=>{
  await fixture(page,{tree:{...baseline,children:[file('.env')]}});
  await expect(page.getByText(/Only hidden files/)).toBeVisible();
  await expect(page.getByLabel('File root location')).toHaveText('/tmp/workspace');
  await page.getByRole('checkbox',{name:'Show hidden files'}).check();
  await expect(page.getByRole('button',{name:'.env',exact:true})).toBeVisible();
});

test('a vanished file restores focus to search after preview closes',async({page})=>{
  const mutableTree=structuredClone(baseline);
  await fixture(page,{tree:mutableTree});
  await page.getByRole('button',{name:'README.md',exact:true}).click();
  await expect(page.getByRole('button',{name:'Copy content'})).toBeVisible();
  mutableTree.children=mutableTree.children.filter(item=>item.name!=='README.md');
  const response=page.waitForResponse('**/files/tree');
  await changed(page);
  await response;
  await page.keyboard.press('Escape');
  await expect(page.getByRole('textbox',{name:'Find files'})).toBeFocused();
});

test('unsupported binary preview keeps original download available',async({page})=>{
  await fixture(page);
  await page.getByRole('button',{name:'binary.dat',exact:true}).click();
  await expect(preview(page).getByRole('alert')).toContainText('binary file preview is not supported');
  await expect(page.getByRole('button',{name:'Copy content'})).toHaveCount(0);
  await expect(page.getByRole('link',{name:'Download original'})).toHaveAttribute('href',/path=binary.dat&download=1$/);
});
