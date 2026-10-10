import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');
const bytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg==', 'base64');
const sha = value => createHash('sha256').update(value).digest('hex');

async function fixture(page, onInput) {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const main = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 0, pending_inputs: 0, held_inputs: 0 };
  const uploads = new Map();
  await page.addInitScript(() => { Object.defineProperty(window.crypto, 'subtle', { value: undefined }); });
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
    if (path.endsWith('/threads')) return json([main]);
    if (path.endsWith('/events')) return json({ thread: main, events: [], next_sequence: 0, has_more: false });
    if (path.endsWith('/inputs')) return onInput(route, json);
    if (path.endsWith('/artifacts') && route.request().method() === 'POST') {
      const request = route.request().postDataJSON();
      expect(request.visibility).toBe('agent');
      expect(request.manifest.sha256).toBe(sha(bytes));
      const upload = uploads.get(request.request_id) ?? { artifact: { id: request.request_id, state: 'uploading', request }, cursor: 0 };
      uploads.set(request.request_id, upload);
      return json(upload);
    }
    if (path.endsWith('/chunks')) {
      const upload = uploads.get(path.split('/').at(-2));
      const chunk = route.request().postDataJSON();
      const chunkBytes = Buffer.from(chunk.data, 'base64');
      expect(chunk.offset).toBe(upload.cursor);
      expect(chunk.sha256).toBe(sha(chunkBytes));
      upload.cursor += chunkBytes.length;
      return json(upload);
    }
    if (path.endsWith('/commit')) {
      const upload = uploads.get(path.split('/').at(-2));
      expect(upload.cursor).toBe(bytes.length);
      upload.artifact.state = 'ready';
      return json(upload.artifact);
    }
    if (path.endsWith('/download')) return route.fulfill({ contentType: 'application/octet-stream', body: bytes });
    return route.fulfill({ status: 404, body: '{}' });
  });
  return uploads;
}

test('image-only admission retries original references after a lost response and reload on HTTP', async ({ page }) => {
  const requests = [];
  const uploads = await fixture(page, async (route, json) => {
    requests.push(route.request().postDataJSON());
    if (requests.length === 1) return route.abort('failed');
    return json({ id: 'input', request_id: requests.at(-1).request_id, state: 'queued' });
  });
  await page.goto('/t/tenant/agents/agent');
  await page.getByLabel('选择图片', { exact: true }).setInputFiles({ name: 'image.png', mimeType: 'image/png', buffer: bytes });
  await expect(page.getByRole('status').filter({ hasText: '已上传' })).toBeVisible();
  await page.getByRole('button', { name: '发送消息', exact: true }).click();
  await expect(page.getByText(/发送结果尚未确认/)).toBeVisible();
  await page.reload();
  await expect(page.getByRole('img', { name: 'image.png', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '重试发送', exact: true }).click();
  await expect(page.getByRole('list', { name: '待发送图片' })).toHaveCount(0);
  expect(uploads.size).toBe(1);
  expect(requests).toHaveLength(2);
  expect(requests[0]).toEqual(requests[1]);
  expect(requests[0].text).toBe('');
  expect(requests[0].images).toEqual([{ artifact_id: [...uploads.keys()][0], sha256: sha(bytes), media_type: 'image/png', size: bytes.length }]);
});

test('empty composer backspace removes the last image while preserving composition and earlier images', async ({ page }) => {
  await fixture(page, async (route, json) => json({id:'input'}));
  await page.goto('/t/tenant/agents/agent');
  await page.getByLabel('选择图片',{exact:true}).setInputFiles(['first.png','last.png'].map(name=>({name,mimeType:'image/png',buffer:bytes})));
  await expect(page.getByRole('status').filter({hasText:'已上传'})).toHaveCount(2);
  const composer=page.getByRole('textbox',{name:'消息',exact:true});
  await composer.fill('a'); await composer.press('Backspace');
  await expect(composer).toHaveValue('');
  await expect(page.getByRole('button',{name:'移除 last.png',exact:true})).toBeVisible();
  await composer.dispatchEvent('keydown',{key:'Backspace',isComposing:true});
  await expect(page.getByRole('button',{name:'移除 last.png',exact:true})).toBeVisible();
  await composer.press('Backspace');
  await expect(page.getByRole('button',{name:'移除 last.png',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'移除 first.png',exact:true})).toBeVisible();
  await page.reload();
  await expect(page.getByRole('button',{name:'移除 first.png',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'移除 last.png',exact:true})).toHaveCount(0);
});

test('paste and drop use the same ordered image draft, enforce limits and preserve edits after rejection', async ({ page }) => {
  const requests = [];
  await fixture(page, async route => {
    requests.push(route.request().postDataJSON());
    return route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ code: 'media_unavailable' }) });
  });
  await page.goto('/t/tenant/agents/agent');
  for (const kind of ['paste', 'drop']) {
    await page.getByRole('textbox', { name: '消息', exact: true }).evaluate((element, { kind, data }) => {
      const transfer = new DataTransfer();
      transfer.items.add(new File([Uint8Array.from(atob(data), char => char.charCodeAt(0))], `${kind}.png`, { type: 'image/png' }));
      element.dispatchEvent(kind === 'paste' ? new ClipboardEvent('paste', { clipboardData: transfer, bubbles: true, cancelable: true }) : new DragEvent('drop', { dataTransfer: transfer, bubbles: true, cancelable: true }));
    }, { kind, data: bytes.toString('base64') });
  }
  await expect(page.getByRole('status').filter({ hasText: '已上传' })).toHaveCount(2);
  const files = Array.from({ length: 7 }, (_, i) => ({ name: `${i}.png`, mimeType: 'image/png', buffer: bytes }));
  await page.getByLabel('选择图片', { exact: true }).setInputFiles(files);
  await expect(page.getByRole('alert')).toContainText('最多添加 8 张');
  await page.getByRole('button', { name: '移除 paste.png', exact: true }).click();
  await page.getByRole('textbox', { name: '消息', exact: true }).fill('Describe the image');
  await page.getByRole('button', { name: '发送消息', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('图片无法读取');
  await expect(page.getByRole('textbox', { name: '消息', exact: true })).toHaveValue('Describe the image');
  await expect(page.getByRole('button', { name: '移除 drop.png', exact: true })).toBeVisible();
  expect(requests[0].images).toHaveLength(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
