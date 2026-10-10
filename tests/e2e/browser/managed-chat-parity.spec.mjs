import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

async function fixture(page, onInput, multipleTenants = false) {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const main = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 0, pending_inputs: 0, held_inputs: 0 };
  const worker = { ...main, id: 'worker', kind: 'worker', parent_id: 'main', name: 'Research' };
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json(multipleTenants ? [tenant, { ...tenant, id: 'tenant-two', name: 'Second workspace' }] : [tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
    if (path.endsWith('/threads')) return json([main, worker]);
    if (path.endsWith('/events')) return json({ thread: path.includes('/threads/worker/') ? worker : main, events: [], next_sequence: 0, has_more: false });
    if (path.endsWith('/inputs')) return onInput(route, json);
    return route.fulfill({ status: 404, body: '{}' });
  });
}

test('unsent drafts survive Thread navigation, reload and late acceptance without clearing newer edits', async ({ page }) => {
  let release;
  const requests = [];
  await fixture(page, async (route, json) => {
    requests.push(route.request().postDataJSON());
    await new Promise(resolve => { release = resolve; });
    return json({ id: 'input', request_id: requests.at(-1).request_id, state: 'accepted' });
  });
  await page.goto('/t/tenant/agents/agent');
  const composer = page.getByRole('textbox', { name: '消息', exact: true });
  await composer.fill('Main unsent draft');
  await page.getByRole('button', { name: 'Threads', exact: true }).click();
  await page.getByRole('button', { name: 'Research Worker · 就绪', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Research 对话', exact: true })).toBeVisible();
  await composer.fill('Worker unsent draft');
  await page.getByRole('button', { name: 'Threads', exact: true }).click();
  await page.getByRole('button', { name: 'Main Main · 就绪', exact: true }).click();
  await expect(composer).toHaveValue('Main unsent draft');
  await page.getByRole('button', { name: '发送消息', exact: true }).click();
  await expect.poll(() => requests.length).toBe(1);
  await composer.fill('Next Main draft');
  await page.getByRole('button', { name: 'Threads', exact: true }).click();
  await page.getByRole('button', { name: 'Research Worker · 就绪', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Research 对话', exact: true })).toBeVisible();
  await expect(composer).toHaveValue('Worker unsent draft');
  release();
  await page.getByRole('button', { name: 'Threads', exact: true }).click();
  await page.getByRole('button', { name: 'Main Main · 就绪', exact: true }).click();
  await expect(composer).toHaveValue('Next Main draft');
  await expect(page.getByRole('button', { name: '发送消息', exact: true })).toBeEnabled();
  await page.reload();
  await expect(composer).toHaveValue('Next Main draft');
  expect(requests[0].text).toBe('Main unsent draft');
  expect(requests).toHaveLength(1);
});

test('shift enter and composition do not submit; definite failure retains the editable draft', async ({ page }) => {
  const requests = [];
  await fixture(page, async route => {
    requests.push(route.request().postDataJSON());
    return route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ code: 'invalid_request' }) });
  });
  await page.goto('/t/tenant/agents/agent');
  const composer = page.getByRole('textbox', { name: '消息', exact: true });
  await composer.fill('Line one');
  await composer.press('Shift+Enter');
  await composer.pressSequentially('Line two');
  await composer.dispatchEvent('keydown', { key: 'Enter', isComposing: true });
  expect(requests).toHaveLength(0);
  await composer.press('Enter');
  await expect(page.getByRole('alert')).toContainText('请检查输入内容');
  await expect(composer).toHaveValue('Line one\nLine two');
  await expect(composer).toBeEditable();
  expect(requests).toHaveLength(1);
});

test('compact desktop navigation persists but mobile navigation retains names; short viewports keep the composer reachable', async ({ page }) => {
  await fixture(page, async (route, json) => json({ id: 'input', state: 'accepted' }), true);
  await page.goto('/t/tenant/agents/agent');
  const sidebar = page.locator('aside.management-sidebar');
  await page.getByRole('button', { name: '收起导航', exact: true }).click();
  await expect(sidebar.getByRole('link', { name: 'Assistant', exact: true })).toBeVisible();
  expect((await sidebar.boundingBox()).width).toBeLessThan(100);
  await page.reload();
  await expect(page.getByRole('button', { name: '展开导航', exact: true })).toBeVisible();
  await expect(page.locator('.management-topline').getByText('Workspace', { exact: true })).toBeVisible();
  await page.goto('/t/tenant-two/agents/agent');
  await expect(page.locator('.management-topline').getByText('Second workspace', { exact: true })).toBeVisible();
  await page.goto('/t/tenant/agents/agent');
  const composer = page.getByRole('textbox', { name: '消息', exact: true });
  await composer.fill('Preserve draft while the viewport changes');
  for (const size of [{ width: 390, height: 420 }, { width: 844, height: 390 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(size);
    await composer.focus();
    await expect(composer).toHaveValue('Preserve draft while the viewport changes');
    const box = await composer.boundingBox();
    expect(box.y).toBeGreaterThanOrEqual(0);
    expect(box.y + box.height).toBeLessThanOrEqual(size.height);
    const send = await page.getByRole('button', { name: '发送消息', exact: true }).boundingBox();
    expect(send.y + send.height).toBeLessThanOrEqual(size.height);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  await page.getByRole('button', { name: '打开导航', exact: true }).click();
  const drawer = page.getByRole('dialog');
  await expect(drawer.getByText('Tenant 默认配置', { exact: true })).toBeVisible();
  await expect(drawer.getByText('Assistant', { exact: true })).toBeVisible();
  await drawer.getByRole('link', { name: 'Assistant', exact: true }).click();
  await expect(drawer).toHaveCount(0);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByRole('button', { name: '展开导航', exact: true }).click();
  expect((await sidebar.boundingBox()).width).toBeGreaterThan(100);
});
