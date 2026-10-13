import { createRequire } from 'node:module';
const require = createRequire(new URL('../../../frontend/package.json', import.meta.url));
const { expect, test } = require('@playwright/test');

for (const status of [502, 504]) {
  test(`Workspace preserves gateway HTTP ${status} and recovers on explicit retry`, async ({ page }) => {
    const user = { id: 'owner', email: 'owner@example.test', email_verified: true };
    const agent = { id: 'agent', name: 'Assistant', status: 'active' };
    const thread = { id: 'main', agent_id: 'agent', kind: 'main', name: 'Main', retention: 'active', state: 'idle', generation: 1, sequence: 0, pending_inputs: 0, held_inputs: 0 };
    let available = false;
    let reads = 0;
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname;
      const json = value => route.fulfill({ contentType: 'application/json', body: JSON.stringify(value) });
      if (path === '/api/auth/session') return json(user);
      if (path === '/api/config') return json({ email_enabled: false });
      if (path === '/api/tenants') return json([{ id: 'tenant', name: 'Workspace', role: 'member' }]);
      if (path.endsWith('/notifications')) return json({ items: [], unread: 0 });
      if (path.endsWith('/agents/agent')) return json({ agent, owner_id: user.id, can_execute: true });
      if (path.endsWith('/threads')) return json([thread]);
      if (path.endsWith('/events')) return json({ thread, events: [], next_sequence: 0, has_more: false });
      if (path.endsWith('/artifacts')) return json([]);
      if (path.endsWith('/environments')) return available
        ? json([{ id: 'environment', name: 'Hosted', kind: 'hosted', online: true, default: true, working_directory: '/workspace', capabilities: ['files'], authorization_version: 1 }])
        : route.fulfill({ status, contentType: 'text/html', body: '<html>private upstream details</html>' });
      if (path.endsWith('/workspace-reads')) {
        reads++;
        const request = route.request().postDataJSON();
        return json({ ...request, operation_id: request.request_id, state: 'completed', listing: { entries: [{ name: 'proof.txt', path: 'proof.txt', kind: 'file', size: 5 }], next_cursor: '', scan_limited: false } });
      }
      return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
    });
    await page.goto('/t/tenant/agents/agent');
    await page.getByRole('button', { name: '文件与产物' }).click();
    const dialog = page.getByRole('dialog', { name: '文件与产物', exact: true });
    await expect(dialog).toContainText(`请求失败（HTTP ${status}），请稍后重试。`);
    await expect(dialog).not.toContainText('private upstream details');
    expect(reads).toBe(0);
    available = true;
    await dialog.getByRole('button', { name: '重试', exact: true }).click();
    await expect(dialog.getByRole('button', { name: 'proof.txt', exact: true })).toBeVisible();
    expect(reads).toBe(1);
  });
}
