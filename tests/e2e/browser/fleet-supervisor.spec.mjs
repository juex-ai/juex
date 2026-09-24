import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

const agents = [
  { id: "ordinary", name: "Supervisor" },
  { id: "retired", name: "Old support" },
  { id: "support", name: "Renamed support", is_supervisor: true },
].map(agent => ({ enabled: true, autostart: false, binding: "bound", runtime_health: "stopped", workspace: `/tmp/${agent.id}`, ...agent }));

async function fixture(page) {
  await page.addInitScript(() => {
    window.sources = [];
    window.EventSource = class extends EventTarget {
      constructor(url) { super(); this.url = String(url); window.sources.push(this); }
      close() {}
    };
  });
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    const json = body => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json(agents);
    if (path === "/api/fleet/status") return json({ process: { rss_bytes: 1024 } });
    if (path.endsWith("/threads")) return json({ active_threads: [], archived_threads: [] });
    if (path.endsWith("/threads/0")) return json({ id: "0", thread_id: "0", alias: "main", retention_state: "active", execution_state: "idle", items: [], has_more_before: false, event_cursor: "cursor-1", token_usage: { total: {}, by_model: {} } });
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    return route.fulfill({ status: 404, body: "not found" });
  });
}

const sidebar = page => page.getByRole("complementary", { name: "Fleet agents" });
const agentLinks = root => root.getByRole("navigation", { name: "Agents", exact: true }).getByRole("link", { name: /^Open .*, / });

test("Supervisor is distinct and pinned in sidebar, compact mode, and Settings", async ({ page }) => {
  await fixture(page);
  await page.goto("/settings");
  const links = agentLinks(sidebar(page));
  await expect(links).toHaveText([/Renamed support/, /Supervisor/, /Old support/]);
  await expect(links.first()).toHaveAttribute("aria-label", "Open Renamed support, Supervisor, Stopped");
  await expect(links.first().locator("svg")).toBeVisible();
  await expect(sidebar(page).locator('[data-supervisor="true"]')).toHaveCount(1);
  const rows = page.getByTestId("fleet-agent-row");
  await expect(rows.first()).toHaveAttribute("data-supervisor", "true");
  await expect(rows.first().getByText("Supervisor", { exact: true })).toBeVisible();
  await expect(rows.first().getByText("Stopped", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Collapse fleet sidebar" }).click();
  await expect(links.first()).toHaveAttribute("title", "Renamed support: Supervisor · Stopped");
  await expect(links.first().locator("svg")).toBeVisible();
  await links.first().focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/agents\/support\/threads\/0$/);
  await expect(links.first()).toHaveAttribute("aria-current", "true");
});

test("live Supervisor replacement reorders both lists and clears the previous role", async ({ page }) => {
  await fixture(page);
  await page.goto("/settings");
  await expect(agentLinks(sidebar(page)).first()).toHaveAttribute("href", "/agents/support");
  await expect(page.getByTestId("fleet-agent-row")).toHaveCount(3);
  await page.evaluate(next => {
    for (const source of window.sources.filter(source => source.url === "/api/fleet/events")) {
      source.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "fleet.roster", agents: next }) }));
    }
  }, agents.map(agent => ({ ...agent, is_supervisor: agent.id === "retired" })));
  await expect(agentLinks(sidebar(page)).first()).toHaveAttribute("href", "/agents/retired");
  await expect(page.getByTestId("fleet-agent-row").first().getByRole("link", { name: "Old support", exact: true })).toBeVisible();
  await expect(sidebar(page).locator('[data-supervisor="true"]')).toHaveCount(1);
  await page.evaluate(next => {
    for (const source of window.sources.filter(source => source.url === "/api/fleet/events")) {
      source.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "fleet.roster", agents: next }) }));
    }
  }, agents.map(agent => ({ ...agent, is_supervisor: false })));
  await expect(agentLinks(sidebar(page)).first()).toHaveAttribute("href", "/agents/ordinary");
  await expect(sidebar(page).locator('[data-supervisor="true"]')).toHaveCount(0);
});

test("mobile Supervisor link stays recognizable and closes the drawer on navigation", async ({ page }) => {
  await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/settings");
  await page.getByRole("button", { name: "Open fleet agents", exact: true }).click();
  const drawer = page.getByRole("dialog", { name: "Fleet agents", exact: true });
  const link = agentLinks(drawer).first();
  await expect(link).toHaveAttribute("aria-label", "Open Renamed support, Supervisor, Stopped");
  await expect(link.locator("svg")).toBeVisible();
  await expect(link).toBeInViewport();
  await link.click();
  await expect(drawer).not.toBeVisible();
  await expect(page).toHaveURL(/\/agents\/support\/threads\/0$/);
});
