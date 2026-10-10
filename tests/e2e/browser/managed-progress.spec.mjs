import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

async function fixture(page) {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const main = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'running', generation: 1, sequence: 3, pending_inputs: 1, held_inputs: 0 };
  const progress = { attempt_id: 'attempt', turn_id: 'turn', generation: 1, sequence: 3, state: 'running', model: 'fixture:model', started_at: '2026-10-10T00:00:00Z', updated_at: '2026-10-10T00:00:01Z', snapshot: { revision: 1, truncated: false, blocks: [{ kind: 'reasoning', index: 0, text: 'Checking the request' }, { kind: 'text', index: 0, text: 'First part' }] } };
  const events = [{ id: 'input', sequence: 1, kind: 'message.appended', data: { id: 'input', role: 'user', blocks: [{ type: 'text', text: 'Explain' }] } }, { id: 'turn', sequence: 2, kind: 'turn.started', data: { turn_id: 'turn' } }];
  let completed = false;
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
    if (path.endsWith('/events')) return json({ thread: main, events: events.filter(event => event.sequence > Number(url.searchParams.get('after') || 0)), progress: completed ? [] : [progress], next_sequence: main.sequence, has_more: false });
    if (path.endsWith('/cancel')) { progress.state = 'cancelled'; main.state = 'idle'; main.pending_inputs = 0; return json({}); }
    return route.fulfill({ status: 404, body: '{}' });
  });
  return {
    queue(count) { main.queued_inputs = count; main.pending_inputs = count + 1; },
    next() { progress.snapshot.revision++; progress.snapshot.blocks[1].text = 'First part and second part'; },
    finish() { completed = true; main.state = 'idle'; main.pending_inputs = 0; main.sequence = 4; events.push({ id: 'final', sequence: 4, kind: 'message.appended', data: { id: 'attempt', role: 'assistant', model: 'fixture:model', blocks: [{ type: 'text', text: 'First part and second part — finished' }] } }); },
  };
}

test('queue count excludes the active input and disappears when only active work remains', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText('First part', { exact: true })).toBeVisible();
  await expect(page.getByText(/排队中 ·/)).toHaveCount(0);
  state.queue(2);
  await expect(page.getByText('排队中 · 2 条输入', { exact: true })).toBeVisible();
  await page.getByText('排队中 · 2 条输入', { exact: true }).click();
  await expect(page.getByText(/按接收顺序等待，不包括正在执行的输入/)).toBeVisible();
  state.queue(0);
  await expect(page.getByText(/排队中 ·/)).toHaveCount(0);
  await expect(page.getByText('First part', { exact: true })).toBeVisible();
});

test('partial text and reasoning render before completion, survive reload and reconcile to one final answer', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText('First part', { exact: true })).toBeVisible();
  await expect(page.locator('.management-message-status')).toHaveText('正在输出…');
  await page.getByText('工作过程', { exact: true }).click();
  await page.getByText('思考过程', { exact: true }).click();
  await expect(page.getByText('Checking the request', { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText('First part', { exact: true })).toBeVisible();
  state.next();
  await expect(page.getByText('First part and second part', { exact: true })).toBeVisible();
  state.finish();
  await expect(page.getByText('First part and second part — finished', { exact: true })).toHaveCount(1);
  await expect(page.getByText('正在输出…', { exact: true })).toHaveCount(0);
  await expect(page.getByText('First part and second part', { exact: true })).toHaveCount(0);
});

test('stopping preserves a clearly incomplete preview instead of presenting it as a finished reply', async ({ page }) => {
  await fixture(page);
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText('First part', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '停止', exact: true }).click();
  await expect(page.locator('.management-message-status')).toHaveText('未完成 · 已取消');
  await expect(page.getByText('First part', { exact: true })).toBeVisible();
  await expect(page.getByText('正在输出…', { exact: true })).toHaveCount(0);
});

for (const [state, label] of [['running', '正在输出…'], ['cancelled', '未完成 · 已取消'], ['failed', '未完成 · 模型请求失败'], ['unknown', '未完成 · 服务中断，结果未知']]) {
  test(`reasoning-only ${state} state stays visible in the collapsed work group`, async ({ page }) => {
    await fixture(page);
    await page.route('**/events?**', route => route.fulfill({ json: {
      thread: { id: 'main', generation: 1, state: state === 'running' ? 'running' : 'idle', pending_inputs: 0, held_inputs: 0 },
      events: [], next_sequence: 3, has_more: false,
      progress: [{ attempt_id: 'thought', turn_id: 'turn', generation: 1, sequence: 3, state, model: 'fixture:model', snapshot: { revision: 1, blocks: [{ kind: 'reasoning', index: 0, text: 'An incomplete thought' }] } }],
    } }));
    await page.goto('/t/tenant/agents/agent');
    const group = page.locator('.management-work-group');
    await expect(group.locator('summary').first()).toContainText(label);
    await group.locator('summary').first().click();
    await expect(group.locator('.management-tool-row summary')).toContainText(label);
  });
}

test('completion during paginated catch-up retains visible text through an event-page failure', async ({ page }) => {
  await fixture(page);
  let failFinal = true;
  let failures = 0;
  await page.route('**/events?**', route => {
    const after = Number(new URL(route.request().url()).searchParams.get('after'));
    const thread = { id: 'main', generation: 1, state: 'idle', pending_inputs: 0, held_inputs: 0 };
    if (!after) return route.fulfill({ json: { thread, events: [{ id: 'start', sequence: 1, kind: 'turn.started', data: { turn_id: 'turn' } }], next_sequence: 1, has_more: true, progress: [{ attempt_id: 'attempt', turn_id: 'turn', generation: 1, sequence: 3, state: 'running', snapshot: { revision: 1, blocks: [{ kind: 'text', index: 0, text: 'Already visible output' }] } }] } });
    if (after === 1) return route.fulfill({ json: { thread, events: [], next_sequence: 2, has_more: true, progress: [] } });
    if (failFinal) { failures++; return route.fulfill({ status: 503, body: '{}' }); }
    return route.fulfill({ json: { thread, events: [{ id: 'final', sequence: 4, kind: 'message.appended', data: { id: 'attempt', role: 'assistant', blocks: [{ type: 'text', text: 'Completed output' }] } }], next_sequence: 4, has_more: false, progress: [] } });
  });
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText('Already visible output', { exact: true })).toBeVisible();
  await expect(page.getByText('正在同步最终状态…', { exact: true })).toBeVisible();
  await expect(page.getByText('正在输出…', { exact: true })).toHaveCount(0);
  await expect.poll(() => failures).toBeGreaterThan(0);
  failFinal = false;
  await expect(page.getByText('Completed output', { exact: true })).toHaveCount(1);
  await expect(page.getByText('Already visible output', { exact: true })).toHaveCount(0);
});
