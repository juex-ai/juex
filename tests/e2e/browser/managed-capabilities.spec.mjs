import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('Agent settings preserve disabled capabilities through edits and reopen', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'admin' };
  let agent = { id: 'agent', name: 'minima', status: 'active', version: 1, instructions: '', model_id: '', worker_depth: 1, hooks: [], extensions: [], capabilities: { disabled: ['context-control', 'memory', 'mcp', 'notes', 'tasks', 'workers'] } };
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
  await expect(page.getByLabel('Memory 读写与学习')).not.toBeChecked();
  await expect(page.getByLabel('Shell 命令')).toBeChecked();
  await expect(page.getByLabel('Notes 持续工作上下文')).not.toBeChecked();
  await expect(page.getByLabel('Tasks 任务操作与完成门禁')).not.toBeChecked();
  await expect(page.getByLabel('模型主动压缩与重置上下文')).not.toBeChecked();
  await expect(page.getByLabel('Worker 委派')).not.toBeChecked();
  await page.getByLabel('名称', { exact: true }).fill('renamed minima');
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(mutations[0].capabilities.disabled).toEqual(['context-control', 'memory', 'mcp', 'notes', 'tasks', 'workers']);
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await expect(page.getByLabel('Memory 读写与学习')).not.toBeChecked();
  await page.getByLabel('Calendar 日程').uncheck();
  await page.getByLabel('Memory 读写与学习').check();
  await page.getByRole('button', { name: '保存', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(mutations[1].capabilities.disabled).toEqual(['calendar', 'context-control', 'mcp', 'notes', 'tasks', 'workers']);
});
