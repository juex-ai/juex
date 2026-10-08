import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { networkInterfaces } from 'node:os';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('LAN HTTP supports Calendar creation, file upload and copying references', async ({ page }) => {
  const address = Object.values(networkInterfaces()).flat().find(value => value?.family === 'IPv4' && !value.internal)?.address;
  expect(address, 'a non-loopback interface is required to test an insecure browser context').toBeTruthy();
  const origin = `http://${address}:4173`;
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'admin' };
  const agent = { id: 'agent', name: 'Supervisor', status: 'active' };
  const thread = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 0, pending_inputs: 0, held_inputs: 0 };
  const content = Buffer.from('LAN HTTP upload\n');
  const sha256 = createHash('sha256').update(content).digest('hex');
  let schedule, artifact, uploaded;
  await page.route('**/api/**', async route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
    if (path.endsWith('/threads')) return json([thread]);
    if (path.endsWith('/events')) return json({ thread, events: [], next_sequence: 0, has_more: false });
    if (path.endsWith('/calendar')) return json({ enabled: true, epoch: 1, version: 1, schedules: schedule ? 1 : 0, pending: 0 });
    if (path.endsWith('/calendar/changes')) {
      const change = request.postDataJSON();
      schedule = { ...change.definition, id: change.id, version: 1, status: 'active', next_at: new Date(Date.now() + 60_000).toISOString() };
      return json({ schedule_id: schedule.id, version: 1, state: 'active' });
    }
    if (path.endsWith('/schedules')) return json({ schedules: schedule ? [schedule] : [], next: 0 });
    if (path.endsWith('/artifacts') && request.method() === 'GET') return json(artifact ? [artifact] : []);
    if (path.endsWith('/artifacts') && request.method() === 'POST') {
      artifact = { id: 'artifact', scope: { agent_id: 'agent' }, request: request.postDataJSON(), state: 'uploading' };
      return json({ artifact, cursor: 0 });
    }
    if (path.endsWith('/artifact/chunks')) {
      uploaded = request.postDataJSON();
      return json({ artifact, cursor: content.length });
    }
    if (path.endsWith('/artifact/commit')) {
      artifact.state = 'ready';
      return json(artifact);
    }
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto(`${origin}/t/tenant/calendar`);
  expect(await page.evaluate(() => ({ secure: window.isSecureContext, uuid: typeof crypto.randomUUID, digest: typeof crypto.subtle })))
    .toEqual({ secure: false, uuid: 'undefined', digest: 'undefined' });
  await page.getByRole('button', { name: '创建日程' }).click();
  await page.getByLabel('日程名称').fill('HTTP Calendar');
  await page.getByLabel('处理方式').selectOption('main');
  await page.getByLabel('执行任务').fill('Use retained Main context');
  await page.getByRole('button', { name: '保存日程' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(schedule.id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  await page.goto(`${origin}/t/tenant/agents/agent?thread=main`);
  await page.getByRole('button', { name: '文件与产物' }).click();
  await page.getByLabel('选择文件').setInputFiles({ name: 'http.txt', mimeType: 'text/plain', buffer: content });
  await page.getByRole('button', { name: '上传文件' }).click();
  await expect(page.getByText('http.txt 已上传。可复制文件引用发送给 Agent。')).toBeVisible();
  expect(artifact.request.manifest.sha256).toBe(sha256);
  expect(uploaded.sha256).toBe(sha256);
  expect(Buffer.from(uploaded.data, 'base64')).toEqual(content);
  await page.getByRole('button', { name: '引用', exact: true }).click();
  await expect(page.getByText('文件引用已复制，可粘贴到对话中。')).toBeVisible();
});
