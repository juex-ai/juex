import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('Agent instruction sources survive ordinary edits, disable and reopen', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'admin' };
  let agent = { id: 'agent', name: 'Supervisor', status: 'active', version: 1, instructions: 'Static guidance', model_id: '', worker_depth: 1, hooks: [], extensions: [], capabilities: { disabled: [] }, dynamic_instructions: { enabled: true, global_path: '/home/owner/AGENTS.md' } };
  const mutations = [];
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/models') || path.endsWith('/devices') || path.endsWith('/purges')) return json([]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/agents/agent') && route.request().method() === 'PUT') {
      const change = route.request().postDataJSON();
      mutations.push(change);
      agent = { ...agent, ...change, version: agent.version + 1 };
      return json(agent);
    }
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto('/t/tenant/fleet');
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await expect(page.getByLabel('读取 AGENTS.md')).toBeChecked();
  await expect(page.getByLabel('全局指令文件（可选）')).toHaveValue('/home/owner/AGENTS.md');
  await page.getByLabel('名称', { exact: true }).fill('renamed Supervisor');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(mutations[0].dynamic_instructions).toEqual({ enabled: true, global_path: '/home/owner/AGENTS.md' });
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await page.getByLabel('全局指令文件（可选）').fill('/home/owner/shared/AGENTS.md');
  await page.getByLabel('读取 AGENTS.md').uncheck();
  await expect(page.getByLabel('全局指令文件（可选）')).toBeDisabled();
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(mutations[1].dynamic_instructions).toEqual({ enabled: false, global_path: '/home/owner/shared/AGENTS.md' });
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await expect(page.getByLabel('读取 AGENTS.md')).not.toBeChecked();
  await expect(page.getByLabel('全局指令文件（可选）')).toHaveValue('/home/owner/shared/AGENTS.md');
  await page.getByLabel('读取 AGENTS.md').check();
  await page.getByLabel('全局指令文件（可选）').fill('');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(mutations[2].dynamic_instructions).toEqual({ enabled: true, global_path: '' });
});
