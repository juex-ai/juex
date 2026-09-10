import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function runtimeFixture(page, { enabled = false, mcpError = false } = {}) {
  let observableReads = 0;
  let runtimeReads = 0;
  const status = {
    start_time: "2026-09-07T00:00:00Z", work_dir: "/tmp/runtime-modules",
    modules: enabled ? ["shell", "mcp", "skills", "hooks", "observables"].map((id) => ({ id, scope: "runtime" })) : [],
    provider: { capabilities: { tools: true } }, shell: {}, sandbox: { enabled: false },
    extensions: { enabled, count: 0, items: [] }, tools: { count: 0, groups: [] },
    mcp: { configured: mcpError ? 1 : 0, connected: 0, errors: mcpError ? 1 : 0,
      servers: mcpError ? [{ name: "failed", type: "http", status: "error", error: "connection refused", connected: false, tool_count: 0 }] : [] },
    hooks: { configured: 0, commands: [] }, skills: { count: 0, items: [], prompt: {} },
  };
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path.endsWith("/events") || path.endsWith("/resource-events")) return route.abort();
    if (path === "/api/agents") return json([{ id: "test-agent", name: "Runtime Agent", workspace: status.work_dir, enabled: true,
      autostart: false, binding: "bound", runtime_health: "healthy", runtime_present: true, process_alive: true, endpoint_reachable: true, endpoint_matched: true }]);
    if (path.endsWith("/runtime")) { runtimeReads++; return json(status); }
    if (path.includes("/observables")) { observableReads++; return json({ observables: [] }); }
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  return { observableReads: () => observableReads, runtimeReads: () => runtimeReads };
}

test("disabled runtime modules are explicit and direct Observable routes do not load resources", async ({ page }) => {
  const reads = await runtimeFixture(page);
  await page.goto("/agents/test-agent/runtime");
  for (const label of ["MCP", "Skills", "Hooks"]) {
    await expect(page.getByText(`${label} module is disabled.`, { exact: true })).toBeVisible();
  }
  await page.getByRole("combobox", { name: "Runtime section" }).click();
  await expect(page.getByRole("option", { name: "Observables" })).toHaveAttribute("aria-disabled", "true");
  await expect(page.getByRole("option", { name: "Extensions" })).toHaveAttribute("aria-disabled", "true");
  await page.keyboard.press("Escape");
  expect(reads.runtimeReads()).toBe(1);
  for (const suffix of ["observables", "observables/hidden", "extensions"]) {
    await page.goto(`/agents/test-agent/runtime/${suffix}`);
    await expect(page.getByText(/module is disabled for this Agent\./)).toBeVisible();
  }
  expect(reads.observableReads()).toBe(0);
});

test("enabled empty catalogs remain distinguishable and share the runtime snapshot", async ({ page }) => {
  const reads = await runtimeFixture(page, { enabled: true });
  await page.goto("/agents/test-agent/runtime");
  await expect(page.getByText("No MCP servers configured.", { exact: true })).toBeVisible();
  await page.getByRole("combobox", { name: "Runtime section" }).click();
  await page.getByRole("option", { name: "Extensions", exact: true }).click();
  await expect(page.getByText("No Extensions are selected for this Agent.", { exact: true })).toBeVisible();
  expect(reads.runtimeReads()).toBe(1);
});

test("enabled MCP errors remain visible", async ({ page }) => {
  await runtimeFixture(page, { enabled: true, mcpError: true });
  await page.goto("/agents/test-agent/runtime");
  await expect(page.getByText("0/1 connected, 1 error", { exact: true })).toBeVisible();
  await expect(page.getByText("connection refused", { exact: true }).first()).toBeVisible();
});

test("runtime catalog failure is an error and leaves configuration reachable", async ({ page }) => {
  await runtimeFixture(page);
  await page.route("**/api/runtime", (route) => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "catalog_error", message: "invalid enabled resource" }) }));
  await page.goto("/agents/test-agent/runtime");
  await expect(page.getByRole("alert")).toContainText("Runtime status is unavailable");
  await page.getByRole("combobox", { name: "Runtime section" }).click();
  await expect(page.getByRole("option", { name: "Config", exact: true })).not.toHaveAttribute("aria-disabled", "true");
});

test("configuration and logs do not request the execution catalog", async ({ page }) => {
  const reads = await runtimeFixture(page);
  for (const section of ["config", "logs"]) {
    await page.goto(`/agents/test-agent/runtime/${section}`);
    await expect(page.getByRole("combobox", { name: "Runtime section" })).toBeVisible();
    await expect(page.getByRole("heading", { name: section === "config" ? "Agent config" : "Agent logs", exact: true })).toBeVisible();
  }
  expect(reads.runtimeReads()).toBe(0);
});

for (const width of [1440, 820, 390, 320]) {
  test(`runtime selector is bounded and keyboard navigable at ${width}px`, async ({ page }) => {
    await runtimeFixture(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/agents/test-agent/runtime');
    const trigger = page.getByRole('combobox', { name: 'Runtime section' });
    await trigger.focus();
    await page.keyboard.press('Enter');
    const current = page.getByRole('option', { name: 'Overview', exact: true });
    await expect(current).toBeFocused();
    await expect(current).toHaveAttribute('data-state', 'checked');
    const bounds = await page.getByRole('listbox').boundingBox();
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
    await page.keyboard.press('ArrowDown');
    await expect(page.getByRole('option', { name: 'Logs', exact: true })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/runtime\/logs$/);
    await expect(trigger).toHaveText('Logs');
    await expect(trigger).toBeFocused();
    await trigger.click();
    await page.keyboard.press('Escape');
    await expect(trigger).toBeFocused();
    await expect(page.getByRole('link', { name: 'Thread Explorer' })).toBeVisible();
  });
}
