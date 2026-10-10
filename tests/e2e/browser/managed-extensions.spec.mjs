import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

for (const sourceKind of ['', 'skills']) test(`${sourceKind || 'Extension'} inspection retries completed failures, polls accepted, and saves receipt identities`, async ({ page }) => {
  const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
  const tenant = { id: 'tenant', name: 'Workspace', role: 'admin' };
  const agent = { id: 'agent', name: 'Assistant', status: 'active', version: 1, instructions: '', configuration: {}, hooks: [], extensions: [] };
  const fleet = { fleet: { id: 'fleet' }, owner: user, membership: { status: 'active' }, settings: {}, agents: [agent] };
  const catalog = { revision: 'receipt-revision', skills: [{ id: 'guide', content: 'saved instructions' }], manifest: { manifest_version: 2, name: 'example', version: '1', skills: [{ id: 'guide', description: 'Read the guide', path: 'SKILL.md' }], hooks: [], mcp: [], observables: [] } };
  if(sourceKind){catalog.source_kind=sourceKind;catalog.skipped=[{path:'linked-skill',reason:'symbolic link',target:'/plugins/linked-skill'}]}
  const requests = [], mutations = [];
  let polls = 0;
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    const json = value => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(value) });
    if (path === '/api/auth/session') return json(user);
    if (path === '/api/config') return json({ email_enabled: false });
    if (path === '/api/tenants') return json([tenant]);
    if (path.endsWith('/fleet')) return json(fleet);
    if (path.endsWith('/models') || path.endsWith('/devices') || path.endsWith('/purges')) return json([]);
    if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
    if (path.endsWith('/environments')) return json([{ id: 'device', name: 'My Mac', online: true, capabilities: ['files', 'shell'] }]);
    if (path.endsWith('/extension-inspections')) {
      const request = route.request().postDataJSON(); requests.push(request);
      return json({ operation_id: request.request_id, environment_id: 'device', directory: '/workspace/extension', state: requests.length === 1 ? 'failed' : 'accepted', error: requests.length === 1 ? 'manifest missing' : undefined });
    }
    if (path.includes('/extension-inspections/device/')) {
      polls++;
      return json({ operation_id: requests.at(-1).request_id, environment_id: 'device', directory: '/workspace/extension', state: polls === 1 ? 'accepted' : 'completed', catalog: polls > 1 ? catalog : null });
    }
    if (path.includes('/extensions/')) {
      const change = route.request().postDataJSON(); mutations.push(change);
      agent.version++;
      agent.extensions = [{ id: path.split('/').at(-1), enabled: change.enabled, resources: change.resources, directory: '/workspace/extension', environment_id: 'device', catalog }];
      return json(agent);
    }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.goto('/t/tenant/fleet');
  await page.getByRole('button', { name: '扩展', exact: true }).click();
  if(sourceKind) await page.getByRole('combobox',{name:'来源类型'}).selectOption('skills');
  await page.getByRole('textbox', { name: /^扩展目录/ }).fill('/workspace/extension');
  await page.getByRole('button', { name: '读取扩展目录', exact: true }).click();
  await expect(page.getByText('manifest missing', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: '读取扩展目录', exact: true }).click();
  await expect(page.getByRole('button', { name: '保存所选资源' })).toBeVisible({ timeout: 8000 });
  expect(requests).toHaveLength(2);
  expect(requests[0].request_id).not.toBe(requests[1].request_id);
  expect(requests[1].source_kind).toBe(sourceKind||undefined);
  if(sourceKind) await expect(page.getByText('linked-skill · /plugins/linked-skill',{exact:true})).toBeVisible();
  expect(polls).toBeGreaterThanOrEqual(2);
  await page.getByRole('button', { name: '保存所选资源' }).click();
  await expect(page.getByText('扩展配置已保存', { exact: true })).toBeVisible();
  expect(mutations[0].inspection_id).toBe(requests[1].request_id);
  expect(mutations[0].resources).toEqual(['skill/guide']);
  expect(mutations[0].catalog).toBeUndefined();
  await page.getByRole('button', { name: '停用扩展' }).click();
  await expect(page.getByRole('button', { name: sourceKind?'重新检查并启用':'启用扩展' })).toBeVisible();
  expect(mutations[1].version).toBe(2);
  expect(mutations[1].enabled).toBe(false);
  if(sourceKind){
    await page.getByRole('button',{name:'重新检查并启用'}).click();
    expect(mutations).toHaveLength(2);
    await expect(page.getByRole('combobox',{name:'来源类型'})).toHaveValue('skills');
    await page.getByRole('button',{name:'读取扩展目录',exact:true}).click();
    await expect(page.getByRole('button',{name:'保存所选资源'})).toBeVisible();
    await page.getByRole('button',{name:'保存所选资源'}).click();
    await expect.poll(()=>mutations.length).toBe(3);
    expect(mutations[2].inspection_id).toBe(requests[2].request_id);
    expect(mutations[2].inspection_id).not.toBe(mutations[0].inspection_id);
  }
});
