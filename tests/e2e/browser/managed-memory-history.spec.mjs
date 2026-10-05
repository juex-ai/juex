import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('historical human Memory receipts show administration without invented source links', async ({ page }, testInfo) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active' };
  const reviews = [
    { id: 'human', agent_id: '', thread_id: '', state: 'applied', committed: true, attempts: 1, updated_at: '2026-09-20T01:02:03Z', reason: '管理员删除了旧知识' },
    { id: 'agent-review', agent_id: 'agent', thread_id: 'main', state: 'applied', committed: true, attempts: 1, updated_at: '2026-09-20T01:02:03Z', reason: '审核通过并保存偏好' },
  ];
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    const json = value => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/fleet')) return json({ owner: user, agents: [agent], membership: { status: 'active' } });
    if (path.endsWith('/memory')) return json({ enabled: true, strategy: 'basic', entries: 1, pending: 0, epoch: 1, version: 1, fence: 1 });
    if (path.endsWith('/entries')) return json({ entries: [], next: 0 });
    if (path.endsWith('/reviews')) return json({ reviews, next: 0 });
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.goto('/t/tenant/memory');
  await page.getByRole('button', { name: '审核记录', exact: true }).click();
  const human = page.locator('article').filter({ hasText: '管理员删除了旧知识' });
  await expect(human.getByRole('heading', { name: '管理操作已完成', exact: true })).toBeVisible();
  await expect(human).toContainText('人类管理');
  await expect(human).not.toContainText('已记住');
  await expect(human).not.toContainText('已移除的 Agent');
  await expect(human.getByRole('link')).toHaveCount(0);
  const ordinary = page.locator('article').filter({ hasText: '审核通过并保存偏好' });
  await expect(ordinary.getByRole('heading', { name: '已记住', exact: true })).toBeVisible();
  await expect(ordinary.getByRole('link', { name: '来源对话' })).toHaveAttribute('href', '/t/tenant/agents/agent?thread=main');
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('memory-history-mobile.png'), fullPage: true });
});
