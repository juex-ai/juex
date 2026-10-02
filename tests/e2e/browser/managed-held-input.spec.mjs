import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('idle held input remains actionable and cancellation reconciles its original history', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const thread = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 2, pending_inputs: 0, held_inputs: 1 };
  const events = [
    { id: 'accepted', sequence: 1, kind: 'input.accepted', data: { receipt: { id: 'input' }, text: 'Original user work' } },
    { id: 'held', sequence: 2, kind: 'input.held', data: { input_id: 'input', reason: 'model_unavailable' } },
  ];
  let cancellations = 0;
  let submitted = 0;
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
    if (path.endsWith('/cancel')) {
      cancellations++;
      thread.held_inputs = 0;
      thread.sequence++;
      events.push({ id: 'cancelled', sequence: thread.sequence, kind: 'thread.cancelled', data: {} });
      return json({});
    }
    if (path.endsWith('/inputs')) { submitted++; return json({}); }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText('Original user work', { exact: true })).toBeVisible();
  await expect(page.getByText(/有 1 条输入已暂停/)).toBeVisible();
  await page.getByRole('button', { name: '停止并放弃暂停输入', exact: true }).click();
  await expect(page.getByRole('button', { name: '停止并放弃暂停输入', exact: true })).toHaveCount(0);
  await expect(page.getByText('已取消', { exact: true })).toBeVisible();
  await expect(page.getByText('Original user work', { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText('已取消', { exact: true })).toBeVisible();
  expect(cancellations).toBe(1);
  expect(submitted).toBe(0);
});
