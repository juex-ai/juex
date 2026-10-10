import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('Thread status reads owned Notes, Tasks, request estimates and incomplete usage', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const thread = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 2, sequence: 10, pending_inputs: 0, held_inputs: 0 };
  let request = { model: 'real-model', provider: 'provider', generation: 2, purpose: 'turn', recorded_at: '2026-10-10T01:00:00Z', estimated_tokens: 640, context_window: 32000, output_reserve: 4000, breakdown: [{ key: 'system', label: 'System', tokens: 140 }], system: 'Keep all acceptance criteria', tools: [{ name: 'read', description: 'Read a file', schema: { type: 'object' } }], message_count: 6 };
  let reads = 0;
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
    if (path.endsWith('/threads')) return json([thread]);
    if (path.endsWith('/events')) return json({ thread, events: [], next_sequence: 10, has_more: false });
    if (path.endsWith('/inspection')) { reads++; return json({ thread, capabilities: { disabled: ['notes'] }, state: { notes: { content: 'Preserve this requirement' }, tasks: [{ id: 'task', title: 'Verify deployment', description: '', acceptance: 'Both hosts work', status: 'doing', priority: 'high', continuation_count: 2 }], revision: 4 }, latest_request: request, imported_history: true, usage: { attempts: 3, reported: 1, partial: 1, unknown: 1, input_tokens: 12, output_tokens: 4, cached_input_tokens: 3 } }); }
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto('/t/tenant/agents/agent');
  await page.getByRole('button', { name: '状态与上下文', exact: true }).click();
  await expect(page.getByText('Preserve this requirement', { exact: true })).toBeVisible();
  await expect(page.getByText(/Notes 已停用/)).toBeVisible();
  await page.getByRole('tab', { name: 'Tasks · 1', exact: true }).click();
  await expect(page.getByText('Verify deployment', { exact: true })).toBeVisible();
  await expect(page.getByText('Both hosts work', { exact: false })).toBeVisible();
  await page.getByRole('tab', { name: '上下文', exact: true }).click();
  await expect(page.getByRole('progressbar', { name: '估算上下文占用' })).toHaveAttribute('value', '640');
  await page.getByText('System 指令', { exact: true }).click();
  await expect(page.getByText('Keep all acceptance criteria', { exact: true })).toBeVisible();
  request = null;
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page.getByText('本代尚无模型请求', { exact: true })).toBeVisible();
  await page.getByRole('tab', { name: '用量', exact: true }).click();
  await expect(page.getByText(/包含迁入历史/)).toBeVisible();
  await expect(page.getByText(/完整报告 1 次 · 部分报告 1 次 · 未知 1 次/)).toBeVisible();
  expect(reads).toBeGreaterThanOrEqual(2);
});
