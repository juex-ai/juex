import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

const item = (id, alias, parent, archived = false) => ({
  thread_id: id, alias, parent_thread_id: parent, retention_state: archived ? "archived" : "active",
  execution_state: archived ? undefined : "idle", created_at: "2026-09-09T00:00:00Z", last_activity_at: "2026-09-09T00:00:00Z",
  pending_input_count: 0, turn_count: 12, generation_count: 3, current_generation_id: "g1", current_context_tokens: 123456,
  token_usage: { total: { input_tokens: 3300000, output_tokens: 12345 }, by_model: {} }, thread_revision: 1,
});

async function fixture(page, options = {}) {
  const rows = [item("0", "main"), item("child", "reviewer", "old"), item("orphan", "missing parent", "absent"),
    ...Array.from({ length: 24 }, (_, i) => item(`filler${i}`, `worker ${i}`, "0")), item("old", "archived parent", "0", true)];
  if (options.longAlias) rows.find((r) => r.thread_id === "child").alias = options.longAlias;
  await page.addInitScript(() => {
    window.sources = [];
    window.EventSource = class extends EventTarget {
      static OPEN = 1; static CLOSED = 2; static CONNECTING = 0;
      constructor(url) { super(); this.url = String(url); window.sources.push(this); }
      readyState = 1; close() { this.readyState = 2; }
    };
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json(["agent-a", "agent-b"].map((id) => ({
      id, name: id === "agent-a" ? "debaga" : "other", workspace: "/tmp/thread-navigation", enabled: true,
      binding: "bound", runtime_health: options.stopped ? "stopped" : "healthy", runtime_present: true,
      activity: { state: "working" },
    })));
    if (path.endsWith("/threads")) return json({ active_threads: rows.filter((r) => r.retention_state === "active"), archived_threads: rows.filter((r) => r.retention_state === "archived") });
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/context")) return json({ messages: [], estimated_tokens: 0 });
    const id = path.match(/\/threads\/([^/]+)/)?.[1];
    if (path.endsWith("/status")) return json({ thread: { id, alias: options.staleAlias, state: id === "child" ? "turn_active" : "idle", working: id === "child", pending_count: 0, can_accept_input: true }, tools: [], token_usage: {} });
    if (/\/threads\/[^/]+$/.test(path)) {
      if (options.hold && id === "child") await options.hold;
      return json({ ...rows.find((r) => r.thread_id === id), dir: `/tmp/${id}`, revision: 1, generation_id: "g1", items: [], has_more_before: false, event_cursor: "cursor-1" });
    }
    return route.fulfill({ status: 404, body: "not found" });
  });
}

test("compact flat rows locate archived parents with focus and resettable highlight", async ({ page }) => {
  await fixture(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/agents/agent-a/threads");
  const main = page.locator('[data-thread-id="0"]');
  await expect(main).toBeVisible();
  const box = await main.boundingBox();
  expect(box.height).toBeGreaterThanOrEqual(48);
  expect(box.height).toBeLessThanOrEqual(56);
  await expect(page.getByRole("button", { name: "parent → #absent" })).toBeDisabled();
  const marker = page.getByRole("button", { name: "parent → archived parent · #old", exact: true });
  await marker.focus();
  await page.keyboard.press("Enter");
  const parent = page.locator('[data-thread-id="old"]');
  await expect(parent).toBeFocused();
  await expect(parent).toBeInViewport();
  await expect(parent).toHaveAttribute("data-highlighted", "true");
  await expect(page).toHaveURL(/\/threads$/);
  await page.clock.install();
  await marker.click();
  await page.clock.fastForward(2000);
  await marker.click();
  await page.clock.fastForward(2000);
  await expect(parent).toHaveAttribute("data-highlighted", "true");
  await page.clock.fastForward(1100);
  await expect(parent).toHaveAttribute("data-highlighted", "false");
  await marker.click();
  await parent.getByRole("button", { name: "parent → main · #0", exact: true }).click();
  await expect(main).toBeFocused();
  await expect(main).toHaveAttribute("data-highlighted", "true");
  await expect(parent).toHaveAttribute("data-highlighted", "false");
});

test("long identities remain compact and reachable on a narrow viewport", async ({ page }) => {
  const alias = "long reviewer name ".repeat(12);
  await fixture(page, { longAlias: alias });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/agents/agent-a/threads");
  const row = page.locator('[data-thread-id="child"]');
  await expect(row).toBeVisible();
  const link = row.getByRole("link");
  await expect(link).toHaveAttribute("title", `${alias.trim()} · #child`);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await link.click();
  const header = page.locator("header");
  await expect(header).toContainText(alias.trim());
  expect((await header.boundingBox()).height).toBe(52);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await expect(header.getByRole("link", { name: "Runtime", exact: true })).toBeInViewport();
  await expect(header.getByLabel("Current Thread status")).toBeInViewport();
});

test("fixed-height header follows the viewed Thread independently of Agent activity", async ({ page }) => {
  await fixture(page);
  await page.goto("/agents/agent-a/threads/0");
  const header = page.locator("header");
  await expect(header).toContainText("main · #0");
  await expect(header.getByLabel("Current Thread status")).toHaveText("Idle");
  expect((await header.boundingBox()).height).toBe(52);
  await page.getByRole("link", { name: "Thread Explorer", exact: true }).click();
  await expect(header).toContainText("Threads");
  await expect(header.getByLabel("Current Thread status")).toHaveCount(0);
  await page.getByRole("link", { name: "reviewer · #child", exact: true }).click();
  await expect(header).toContainText("reviewer · #child");
  await expect(header.getByLabel("Current Thread status")).toHaveText("Working");
  await page.getByRole("link", { name: "Thread Explorer", exact: true }).click();
  await page.getByRole("link", { name: "archived parent · #old", exact: true }).click();
  await expect(header.getByLabel("Current Thread status")).toHaveText("Archived");
  await header.getByRole("link", { name: "Runtime", exact: true }).click();
  await expect(header).toContainText("Runtime");
  await expect(header.getByLabel("Current Thread status")).toHaveCount(0);
  await expect(header.getByRole("link", { name: "Runtime", exact: true })).toHaveAttribute("aria-current", "page");
  await header.getByRole("link", { name: "Chat with debaga" }).click();
  await expect(header).toContainText("main · #0");
});

test("loading and stopped Threads do not inherit an idle or previous status", async ({ page }) => {
  let release;
  await fixture(page, { hold: new Promise((resolve) => { release = resolve; }) });
  await page.goto("/agents/agent-a/threads/0");
  await expect(page.locator("header")).toContainText("main · #0");
  await page.getByRole("link", { name: "Thread Explorer", exact: true }).click();
  await page.getByRole("link", { name: "reviewer · #child", exact: true }).click();
  await expect(page.locator("header")).not.toContainText("main · #0");
  await expect(page.locator("header").getByLabel("Current Thread status")).toHaveCount(0);
  release();
  await expect(page.locator("header")).toContainText("reviewer · #child");
  await page.unrouteAll();
  await fixture(page, { stopped: true });
  await page.goto("/agents/agent-a/threads/0");
  await expect(page.locator("header").getByLabel("Current Thread status")).toHaveText("Unknown");
});


test("header prefers current metadata alias and marks disconnected status unknown", async ({ page }) => {
  await fixture(page, { staleAlias: "old alias" });
  await page.goto("/agents/agent-a/threads/child");
  const header = page.locator("header");
  await expect(header.getByLabel("Current Thread status")).toHaveText("Working");
  await expect(header).toContainText("reviewer · #child");
  await expect(header).not.toContainText("old alias");
  await page.evaluate(() => {
    const source = window.sources.find((s) => s.url.includes("/threads/child/events") && s.readyState === 1);
    source.readyState = 0;
    source.onerror?.(new Event("error"));
    source.dispatchEvent(new Event("error"));
  });
  await expect(header.getByLabel("Current Thread status")).toHaveText("Unknown");
  await page.evaluate(() => {
    const source = window.sources.find((s) => s.url.includes("/threads/child/events") && s.readyState === 0);
    source.readyState = 1;
    source.onopen?.(new Event("open"));
    source.dispatchEvent(new Event("open"));
  });
  await expect(header.getByLabel("Current Thread status")).toHaveText("Working");
});

test("switching Agents with the same Thread ID resets the header while loading", async ({ page }) => {
  await fixture(page);
  let release;
  const hold = new Promise((resolve) => { release = resolve; });
  await page.route(/\/agents\/agent-b\/api\/threads\/0$/, async (route) => {
    await hold;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({
      ...item("0", "other main"), dir: "/tmp/other", revision: 1, generation_id: "g1",
      items: [], has_more_before: false, event_cursor: "cursor-1",
    }) });
  });
  await page.goto("/agents/agent-a/threads/0");
  const header = page.locator("header");
  await expect(header).toContainText("main · #0");
  await page.evaluate(() => {
    history.pushState({}, "", "/agents/agent-b/threads/0");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(header).toContainText("other");
  await expect(header).not.toContainText("main · #0");
  release();
  await expect(header).toContainText("other main · #0");
  await expect(header).not.toContainText("debaga");
});
