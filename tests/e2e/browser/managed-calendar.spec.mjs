import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

test('Calendar Main delivery and recovery controls preserve their distinct meaning', async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'admin' };
  const agent = { id: 'agent', name: 'Supervisor', status: 'active' };
  let schedule;
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/fleet')) return json({ owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] });
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/calendar')) return json({ enabled: true, epoch: 1, version: 1, schedules: schedule ? 1 : 0, pending: 0 });
    if (path.endsWith('/calendar/changes')) {
      const request = route.request().postDataJSON();
      schedule = { ...request.definition, id: request.id, version: 1, status: 'active', next_at: new Date(Date.now() + 60_000).toISOString() };
      return json({ schedule_id: schedule.id, version: 1, state: 'active' });
    }
    if (path.endsWith('/schedules')) return json({ schedules: schedule ? [schedule] : [], next: 0 });
    if (path.endsWith('/occurrences')) return json({ occurrences: schedule ? [{ ...schedule, id: 'occurrence', schedule_id: schedule.id, state: 'accepted', main_thread_id: 'main', input_id: 'input', scheduled_at: new Date().toISOString() }] : [], next: 0 });
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto('/t/tenant/calendar');
  await page.getByRole('button', { name: '创建日程' }).click();
  await page.getByLabel('日程名称').fill('Main context reminder');
  await page.getByLabel('处理方式').selectOption('main');
  await page.getByLabel('执行任务').fill('Use retained Main context');
  await page.getByLabel('恢复后补跑策略').selectOption('none');
  await expect(page.getByLabel('故障后允许补跑的分钟数')).toHaveCount(0);
  await expect(page.getByText('送入所选 Agent 的现有 Main 对话。', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: '保存日程' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(schedule.mode).toBe('main');
  expect(schedule.agent_id).toBe('agent');
  expect(schedule.catch_up).toBe('none');
  await expect(page.getByText('Main：Supervisor', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: '编辑', exact: true }).click();
  await expect(page.getByLabel('处理方式')).toHaveValue('main');
  await expect(page.getByLabel('恢复后补跑策略')).toHaveValue('none');
  await page.getByRole('button', { name: '取消', exact: true }).click();
  await page.getByRole('tab', { name: '执行记录' }).click();
  await expect(page.getByText('Main 已接收', { exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: '查看 Main' })).toHaveAttribute('href', '/t/tenant/agents/agent?thread=main');
  await expect(page.getByRole('button', { name: '取消本次执行' })).toHaveCount(0);
  await expect(page.getByText('已送达 Main，执行结果请查看对话。', { exact: false })).toBeVisible();
});
