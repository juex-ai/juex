import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function openModuleThread(page, mode = "ready") {
  let resourceReads = 0;
  const enabled = mode !== "disabled";
  const state = (id, value) => ({ module_id: id, version: 1, revision: id, status: mode === "error" ? "error" : "ready", value,
    resources: [], operations: [], ...(mode === "error" ? { error: "unreadable state" } : {}) });
  const snapshot = { agent_id: "test-agent", thread_id: "0", composition_revision: mode, revision: mode, read_only: false,
    observed_cursor: { generation_id: "g1", seq: 1, offset: 1 },
    modules: enabled ? { goal: state("goal", mode === "empty" ? null : { description: "Verify module views", status: "in_progress" }),
      notes: state("notes", mode === "empty" ? null : { content: "Check scoped resources" }),
      scratchpad: { ...state("scratchpad", null), resources: ["files"] } } : {},
    ui: enabled ? ["goal.status", "notes.status", "scratchpad.files"].map((id) => ({ id, module_id: id.split(".")[0], version: 1 })) : [],
  };
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/events") || path.endsWith("/resource-events")) return route.abort();
    const json = (body) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json([{ id: "test-agent", name: "Module Agent", workspace: "/tmp/module-browser", enabled: true,
      autostart: false, binding: "bound", runtime_health: "healthy", runtime_present: true, process_alive: true, endpoint_reachable: true, endpoint_matched: true }]);
    if (path.endsWith("/modules")) return json(snapshot);
    if (path.endsWith("/resources/files/tree")) { resourceReads++; return json({ name: "scratchpad", path: "/", is_dir: true, children: [{ name: "draft.md", path: "draft.md", is_dir: false }] }); }
    if (path.endsWith("/resources/files/content")) { resourceReads++; return json({ path: "draft.md", content: "Scoped file preview", kind: "text", size: 19, truncated: false }); }
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/context")) return json({ messages: [], estimated_tokens: 0 });
    if (path.endsWith("/status")) return json({ cursor: "cursor-1", thread: { id: "0", alias: "main", state: "idle", working: false, pending_count: 0, max_pending_inputs: 8, can_accept_input: true }, tools: [], token_usage: { input_tokens: 0, output_tokens: 0 } });
    if (path.endsWith("/threads/0")) return json({ thread_id: "0", alias: "main", dir: "/tmp/module-browser/0", retention_state: "active", execution_state: "idle", created_at: "2026-09-07T00:00:00Z", last_activity_at: "2026-09-07T00:00:00Z", revision: 1, generation_id: "g1", turn_count: 0, pending_input_count: 0, items: [], has_more_before: false, event_cursor: "cursor-1" });
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/agents/test-agent/threads/0");
  return () => resourceReads;
}

test("module state loads through the shared snapshot and file resources stay lazy", async ({ page }) => {
  const reads = await openModuleThread(page);
  const badge = page.getByRole("button", { name: /^Open goal and notes:/ });
  await expect(badge).toBeVisible();
  await badge.click();
  await expect(page.getByText("Verify module views", { exact: true })).toBeVisible();
  await expect(page.getByText("Check scoped resources", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  expect(reads()).toBe(0);
  await page.getByRole("button", { name: "Show scratchpad", exact: true }).click();
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  expect(reads()).toBeGreaterThanOrEqual(2);
});

test("disabled composition hides module controls and does not read resource bodies", async ({ page }) => {
  const reads = await openModuleThread(page, "disabled");
  await expect(page.getByRole("group", { name: "Thread status", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open goal and notes:/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Show scratchpad", exact: true })).toHaveCount(0);
  expect(reads()).toBe(0);
});

test("empty state and unreadable state have distinct presentation", async ({ page }) => {
  await openModuleThread(page, "empty");
  await expect(page.getByRole("button", { name: /^Open goal and notes:/ })).toBeVisible();
  await page.unrouteAll();
  await openModuleThread(page, "error");
  await expect(page.getByText("Module state unavailable", { exact: true })).toBeVisible();
});

test("a directory refresh preserves an in-flight module file preview", async ({ page }) => {
  await page.addInitScript(() => {
    const NativeEventSource = window.EventSource;
    window.EventSource = class extends NativeEventSource {
      constructor(url, options) {
        super(url, options);
        if (String(url).endsWith("/resource-events")) window.resourceSource = this;
      }
    };
  });
  await openModuleThread(page);
  let releaseContent;
  let markContentStarted;
  const contentStarted = new Promise((resolve) => { markContentStarted = resolve; });
  const contentReleased = new Promise((resolve) => { releaseContent = resolve; });
  await page.route("**/resources/files/content?*", async (route) => {
    markContentStarted();
    await contentReleased;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({
      path: "draft.md", content: "Preview survives directory refresh", kind: "text", size: 34, truncated: false,
    }) });
  });
  await page.getByRole("button", { name: "Show scratchpad", exact: true }).click();
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await contentStarted;
  const refreshed = page.waitForResponse("**/resources/files/tree");
  await page.evaluate(() => window.resourceSource.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "resource.changed", resources: ["workspace"] }),
  })));
  await refreshed;
  releaseContent();
  await expect(page.getByText("Preview survives directory refresh", { exact: true })).toBeVisible();
});

test("module controls share one Thread subscription", async ({ page }) => {
  await page.addInitScript(() => {
    const NativeEventSource = window.EventSource;
    window.moduleSources = [];
    window.EventSource = class extends NativeEventSource {
      constructor(url, options) {
        super(url, options);
        if (String(url).endsWith("/modules/events")) window.moduleSources.push(this);
      }
    };
  });
  await openModuleThread(page);
  await expect(page.getByRole("button", { name: /^Open goal and notes:/ })).toBeVisible();
  await page.getByRole("button", { name: "Show scratchpad", exact: true }).click();
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.moduleSources.filter((source) => source.readyState !== EventSource.CLOSED).length)).toBe(1);
});
