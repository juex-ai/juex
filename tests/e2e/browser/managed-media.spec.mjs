import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('conversation images and tool attachments load verified bytes or show an explicit failure', async ({ page }, testInfo) => {
  const bytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg==', 'base64');
  const media = { artifact_id: 'image', media_type: 'image/png', sha256: createHash('sha256').update(bytes).digest('hex') };
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const thread = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 5, pending_inputs: 0, held_inputs: 1 };
  const events = [
    { id: 'image', sequence: 1, kind: 'message.appended', data: { id: 'image', role: 'user', blocks: [{ type: 'text', text: 'Preserved image' }, { type: 'image', media }] } },
    { id: 'tool', sequence: 2, kind: 'message.appended', data: { id: 'tool', kind: 'tool_result', role: 'user', blocks: [{ type: 'tool_result', tool_name: 'read', content: 'Tool image result', media }] } },
    { id: 'missing', sequence: 3, kind: 'message.appended', data: { id: 'missing', role: 'user', blocks: [{ type: 'image', media: { ...media, artifact_id: 'deleted' } }] } },
    { id: 'accepted', sequence: 4, kind: 'input.accepted', data: { receipt: { id: 'input' }, text: 'Continue' } },
    { id: 'held', sequence: 5, kind: 'input.held', data: { input_id: 'input', reason: 'media_unavailable' } },
  ];
  let downloads = 0;
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const json = value => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
    if (path.endsWith('/threads')) return json([thread]);
    if (path.endsWith('/events')) return json({ thread, events: events.filter(event => event.sequence > Number(url.searchParams.get('after') || 0)), next_sequence: thread.sequence, has_more: false });
    if (path.endsWith('/artifacts/image/download')) { downloads++; return route.fulfill({ status: 200, headers: { 'content-type': 'application/octet-stream', 'content-disposition': 'attachment; filename="image.png"' }, body: bytes }); }
    if (path.endsWith('/artifacts/deleted/download')) return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText(/历史图片不可用.*本轮已暂停/)).toBeVisible();
  expect(downloads).toBe(0);
  await page.getByRole('button', { name: '查看图片', exact: true }).first().click();
  await expect(page.getByRole('img', { name: '对话图片' })).toBeVisible();
  await expect.poll(() => page.getByRole('img', { name: '对话图片' }).evaluate(img => img.naturalWidth)).toBe(1);
  await page.getByText('执行结果 · read', { exact: true }).click();
  await page.getByRole('button', { name: '查看图片', exact: true }).first().click();
  await expect(page.getByRole('img', { name: '对话图片' })).toHaveCount(2);
  await page.getByRole('button', { name: '查看图片', exact: true }).click();
  await expect(page.getByText('图片暂不可用，可能已删除或访问权限已改变。', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '重试图片', exact: true })).toBeVisible();
  expect(downloads).toBe(2);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('conversation-media-mobile.png'), fullPage: true });
  await page.reload();
  await expect(page.getByRole('img', { name: '对话图片' })).toHaveCount(0);
  expect(downloads).toBe(2);
});
