import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

async function fixture(page, toolBoundary = false) {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', worker_depth: 1 };
  const main = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 40, pending_inputs: 0, held_inputs: 0 };
  const events = Array.from({ length: 40 }, (_, i) => ({ id: `e${i + 1}`, sequence: i + 1, generation: 1, kind: 'message.appended', data: { id: `m${i + 1}`, role: i % 2 ? 'assistant' : 'user', blocks: [{ type: 'text', text: `History ${i + 1}: A retained message with enough text to occupy a normal conversation row.` }] } }));
  const requests = [];
  if (toolBoundary) {
    events[18] = { ...events[18], kind: 'turn.started', data: { turn_id: 'turn' } };
    events[19] = { ...events[19], data: { id: 'call', role: 'assistant', blocks: [{ type: 'reasoning', text: 'Earlier reasoning' }, { type: 'tool_use', tool_use_id: 'read-call', tool_name: 'read', input: { path: '/work/example.txt' } }] } };
    events[20] = { ...events[20], turn_id: 'turn', tool_attempt_id: toolBoundary === 'canonical' ? undefined : 'call', data: { id: 'result', role: 'user', kind: 'tool_result', blocks: [{ type: 'tool_result', tool_use_id: 'read-call', tool_name: 'read', content: 'A retained orphan result' }] } };
  }
  let failOlder = false;
  let delayOlder = null;
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
    if (path.endsWith('/events')) {
      requests.push(url.search);
      if (url.searchParams.has('before')) {
        const before = Number(url.searchParams.get('before'));
        if (before && failOlder) return route.fulfill({ status: 503, body: '{}' });
        const result = { thread: { ...main }, events: before ? events.slice(0, 20) : events.slice(20), next_sequence: main.sequence, previous_sequence: before ? 1 : 21, has_more: false, has_previous: !before, progress: [] };
        if (before && delayOlder) await delayOlder;
        return json(result);
      }
      return json({ thread: main, events: events.filter(event => event.sequence > Number(url.searchParams.get('after'))), next_sequence: main.sequence, has_more: false, progress: [] });
    }
    return route.fulfill({ status: 404, body: '{}' });
  });
  return {
    requests,
    fail(value) { failOlder = value; },
    delay() { let resolve; delayOlder = new Promise(done => { resolve = done; }); return () => { resolve(); delayOlder = null; }; },
    next() { main.sequence = 41; main.state = 'running'; events.push({ id: 'e41', sequence: 41, kind: 'message.appended', data: { id: 'm41', role: 'user', blocks: [{ type: 'text', text: 'A new input during history loading' }] } }); },
  };
}

test('opens recent history, anchors older pages and keeps the live cursor and state independent', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText(/^History 40:/)).toBeVisible();
  await expect(page.getByText(/^History 1:/)).toHaveCount(0);
  expect(state.requests[0]).toContain('before=0');
  expect(state.requests.some(url => url.includes('after=0'))).toBe(false);
  const transcript = page.locator('.management-transcript');
  await transcript.evaluate(element => { element.scrollTop = 0; });
  const anchor = page.getByText(/^History 21:/);
  const y = (await anchor.boundingBox()).y;
  const release = state.delay();
  await page.getByRole('button', { name: '加载更早的消息', exact: true }).click();
  state.next();
  await expect(page.getByText('A new input during history loading', { exact: true })).toHaveCount(1);
  release();
  await expect(page.getByText(/^History 1:/)).toHaveCount(1);
  expect(Math.abs((await anchor.boundingBox()).y - y)).toBeLessThan(3);
  await expect(page.locator('.management-conversation-heading')).toContainText('处理中');
  await expect(page.getByText('A new input during history loading', { exact: true })).toHaveCount(1);
  await expect(page.locator('.management-message')).toHaveCount(41);
  await expect(page.getByRole('button', { name: '加载更早的消息', exact: true })).toHaveCount(0);
});

test('failed older-page loading preserves current messages and can be retried', async ({ page }) => {
  const state = await fixture(page); state.fail(true);
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText(/^History 40:/)).toBeVisible();
  await page.locator('.management-transcript').evaluate(element => { element.scrollTop = 0; });
  await page.getByRole('button', { name: '加载更早的消息', exact: true }).click();
  await expect(page.getByText(/更早的历史未加载，可重试/)).toBeVisible();
  await expect(page.locator('.management-message')).toHaveCount(20);
  state.fail(false);
  await page.getByRole('button', { name: '加载更早的消息', exact: true }).click();
  await expect(page.locator('.management-message')).toHaveCount(40);
  await expect(page.getByText(/更早的历史未加载，可重试/)).toHaveCount(0);
});

for (const identity of ['explicit', 'canonical']) test(`prepending a missing tool request preserves expanded output and anchor (${identity})`, async ({ page }) => {
  await fixture(page, identity);
  await page.goto('/t/tenant/agents/agent');
  await expect(page.getByText(/^History 40:/)).toBeVisible();
  await page.locator('.management-transcript').evaluate(element => { element.scrollTop = 0; });
  await expect(page.locator('.management-work-group > summary')).toContainText('1 项记录');
  await page.locator('.management-work-group > summary').click();
  await page.locator('.management-tool-row > summary').click();
  await expect(page.getByText('A retained orphan result', { exact: true })).toBeVisible();
  const groupY = (await page.locator('.management-work-group > summary').boundingBox()).y;
  await page.getByRole('button', { name: '加载更早的消息', exact: true }).click();
  await expect(page.getByText('A retained orphan result', { exact: true })).toBeVisible();
  await expect(page.locator('.management-tool-row[open] > summary')).toContainText('read');
  expect(Math.abs((await page.locator('.management-work-group > summary').boundingBox()).y - groupY)).toBeLessThan(3);
  await expect(page.getByText('A retained orphan result', { exact: true })).toHaveCount(1);
  await expect(page.locator('.management-work-group > summary')).toContainText('1 次工具调用');
  await expect(page.locator('.management-tool-state')).toHaveCount(1);
  await expect(page.getByText('等待结果', { exact: true })).toHaveCount(0);
});
