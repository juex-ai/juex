import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('new context preserves history and retries an uncertain reset with the same identity', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const thread = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 1, pending_inputs: 0, held_inputs: 0 };
  const events = [{ id: 'history', sequence: 1, kind: 'input.accepted', data: { receipt: { id: 'input' }, text: 'Retained historical work' } }];
  const requests = [];
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
    if (path.endsWith('/threads')) return json([thread]);
    if (path.endsWith('/events')) return json({ thread, events: events.filter(event => event.sequence > Number(url.searchParams.get('after') || 0)), next_sequence: thread.sequence, has_more: false });
    if (path.endsWith('/reset-context')) {
      requests.push(route.request().postDataJSON());
      if (requests.length === 1) { thread.generation = 2; return route.abort('failed'); }
      expect(thread.generation).toBe(2);
      return json(thread);
    }
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto('/t/tenant/agents/agent');
  await page.getByRole('button', { name: '新上下文', exact: true }).click();
  await expect(page.getByText(/未完成 Tasks、完整历史和工作文件保留/)).toBeVisible();
  await page.getByRole('button', { name: '开始新上下文', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('结果尚未确认');
  await page.reload();
  await page.getByRole('button', { name: '新上下文', exact: true }).click();
  await expect(page.getByRole('dialog')).toContainText('结果尚未确认');
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(requests).toHaveLength(2);
  expect(requests[0]).toEqual(requests[1]);
  await expect(page.getByText('Retained historical work', { exact: true })).toBeVisible();
});
