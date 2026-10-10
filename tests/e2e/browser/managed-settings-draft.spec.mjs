import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('extension saves preserve unsaved Agent configuration and use the refreshed version', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'member' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', version: 1, worker_depth: 1, instructions: 'Original', configuration: {}, hooks: [], extensions: [{ id: 'extension', enabled: true, resources: [], environment_id: 'device', directory: '/workspace/example', catalog: { manifest: { name: 'Example', version: '1', skills: [], hooks: [], mcp: [], observables: [] } } }] };
  const saved = [];
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/agents/agent')) {
      if (route.request().method() === 'PUT') { const body = route.request().postDataJSON(); saved.push(body); Object.assign(agent, body, { version: agent.version + 1 }); return json(agent); }
      return json({ agent, owner_id: user.id, can_execute: true });
    }
    if (path.endsWith('/extensions/extension')) { agent.version++; agent.extensions[0].enabled = false; return json(agent); }
    if (path.endsWith('/models') || path.endsWith('/environments')) return json([]);
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto('/t/tenant/agents/agent/settings');
  const instructions = page.getByRole('textbox', { name: '专属指令', exact: true });
  await instructions.fill('My unsaved instructions');
  await page.getByRole('button', { name: '管理扩展资源' }).click();
  await page.getByRole('button', { name: '停用扩展' }).click();
  await expect(page.getByRole('button', { name: '启用扩展' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(instructions).toHaveValue('My unsaved instructions');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByText('Agent 配置已保存。', { exact: true })).toBeVisible();
  expect(saved[0].version).toBe(2);
  expect(saved[0].instructions).toBe('My unsaved instructions');
  await expect(instructions).toHaveValue('My unsaved instructions');
});
