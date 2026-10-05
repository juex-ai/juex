import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('Agent default environment preserves unavailable selections and handles conflicts', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'admin' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', version: 1, instructions: '', model_id: '', hooks: [], extensions: [] };
  const fleet = { owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] };
  let configuration = { environment_id: 'missing', working_directory: '/retained/path', version: 2 };
  const mutations = [];
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    const json = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/fleet')) return json(fleet);
    if (path.endsWith('/models') || path.endsWith('/devices') || path.endsWith('/purges')) return json([]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/environments')) return json([{ id: 'mac', name: 'My Mac', kind: 'native', online: false, default: configuration.environment_id === 'mac', working_directory: '/Users/owner', capabilities: ['files', 'shell'] }]);
    if (path.endsWith('/default-environment')) {
      if (route.request().method() === 'PUT') {
        mutations.push(route.request().postDataJSON());
        if (mutations.length === 1) return json({ error: 'conflict' }, 409);
        configuration = { ...mutations.at(-1), version: 3 };
      }
      return json(configuration);
    }
    return json({}, 404);
  });
  await page.goto('/t/tenant/fleet');
  await page.getByRole('button', { name: '执行环境', exact: true }).click();
  await expect(page.getByLabel('默认执行环境')).toHaveValue('missing');
  await expect(page.getByLabel('默认工作目录')).toHaveValue('/retained/path');
  await expect(page.getByRole('button', { name: '保存执行环境' })).toBeDisabled();
  await expect(page.getByText(/当前默认环境不可用/)).toBeVisible();
  await page.getByLabel('默认执行环境').selectOption('mac');
  await expect(page.getByText(/工作目录只是默认位置/)).toBeVisible();
  await page.getByLabel('默认工作目录').fill('/Users/owner/agent-a');
  await page.getByRole('button', { name: '保存执行环境' }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page.getByLabel('默认工作目录')).toHaveValue('/Users/owner/agent-a');
  expect(mutations[0]).toEqual({ environment_id: 'mac', working_directory: '/Users/owner/agent-a', version: 2 });
  await page.getByRole('button', { name: '取消', exact: true }).click();
  await page.getByRole('button', { name: '执行环境', exact: true }).click();
  await page.getByLabel('默认执行环境').selectOption('');
  await expect(page.getByText('保存后使用部署提供的默认环境。')).toBeVisible();
  await expect(page.getByLabel('默认工作目录')).toHaveCount(0);
  await page.getByRole('button', { name: '保存执行环境' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(mutations[1]).toEqual({ environment_id: '', working_directory: '', version: 2 });
});
