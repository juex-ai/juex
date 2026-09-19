import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

const item = (id, archived = false) => ({
  thread_id: id, alias: id === "0" ? "main" : id, parent_thread_id: id === "0" ? undefined : "0",
  retention_state: archived ? "archived" : "active", execution_state: "idle",
  created_at: "2026-09-19T00:00:00Z", last_activity_at: "2026-09-19T00:00:00Z",
  pending_input_count: 0, turn_count: 1, generation_count: 1, current_generation_id: "g1",
  current_context_tokens: 10, token_usage: { total: { input_tokens: 10, output_tokens: 10 }, by_model: {} }, thread_revision: 1,
});

async function fixture(page, { stopped = false, parentChild = false } = {}) {
  const state = {
    active_threads: [item("0"), item("active-a"), item("active-b")],
    archived_threads: [item("old-a", true), item("old-b", true)],
    calls: [], paths: [], failures: new Set(), holdMutation: undefined, holdList: undefined, heldListStarted: false,
  };
  if (parentChild) {
    state.active_threads[2].parent_thread_id = "active-a";
    state.archived_threads[1].parent_thread_id = "old-a";
  }
  await page.addInitScript(() => {
    window.EventSource = class extends EventTarget { readyState = 1; close() { this.readyState = 2; } };
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json(["agent-a", "agent-b"].map((id) => ({
      id, name: id, workspace: `/tmp/${id}`, enabled: true, binding: "bound",
      runtime_health: stopped ? "stopped" : "healthy", runtime_present: !stopped,
    })));
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/threads")) {
      const snapshot = structuredClone({ active_threads: state.active_threads, archived_threads: state.archived_threads });
      const hold = state.holdList;
      if (hold) { state.holdList = undefined; state.heldListStarted = true; await hold; }
      return json(snapshot);
    }
    const match = path.match(/\/threads\/([^/]+)(\/archive)?$/);
    if (match && ["DELETE", "POST"].includes(route.request().method())) {
      const id = match[1], action = match[2] ? "archive" : "delete";
      state.calls.push({ id, action });
      state.paths.push(path);
      if (state.holdMutation) await state.holdMutation;
      if (state.failures.has(id)) return route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: { message: "Thread is busy" } }) });
      const children = action === "archive" ? state.active_threads : [...state.active_threads, ...state.archived_threads];
      if (children.some((thread) => thread.parent_thread_id === id)) return route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: { message: "Child still references this Thread" } }) });
      if (action === "archive") {
        const index = state.active_threads.findIndex((thread) => thread.thread_id === id);
        state.archived_threads.push({ ...state.active_threads.splice(index, 1)[0], retention_state: "archived" });
      } else state.archived_threads = state.archived_threads.filter((thread) => thread.thread_id !== id);
      return json({});
    }
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.goto("/agents/agent-a/threads");
  await expect(page.getByRole("link", { name: "main · #0", exact: true })).toBeVisible();
  return state;
}

for (const width of [1280, 390]) {
  test(`batch archive excludes Main, supports mixed selection and locks mutations at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    const state = await fixture(page);
    const active = page.getByRole("region", { name: "Active threads", exact: true });
    const all = active.getByRole("checkbox", { name: "Select all active Worker Threads" });
    await expect(active.getByRole("checkbox", { name: "Select main · #0", exact: true })).toBeDisabled();
    await active.getByRole("checkbox", { name: "Select active-a · #active-a", exact: true }).check();
    await expect(all).toHaveJSProperty("indeterminate", true);
    await all.check();
    await expect(active).toContainText("2 selected");
    await expect(page).toHaveURL(/\/agents\/agent-a\/threads$/);
    let release;
    state.holdMutation = new Promise((resolve) => { release = resolve; });
    await active.getByRole("button", { name: "Archive selected", exact: true }).click();
    await expect.poll(() => state.calls.length).toBe(2);
    await expect(all).toBeDisabled();
    await expect(page.getByRole("button", { name: "New Worker", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeDisabled();
    await expect(active.getByRole("button", { name: "Archiving...", exact: true })).toBeDisabled();
    release();
    await expect(active.getByRole("link")).toHaveCount(1);
    await expect(active.getByRole("button", { name: "Archive selected", exact: true })).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Archived threads", exact: true }).getByRole("link")).toHaveCount(4);
    expect(state.calls).toEqual([{ id: "active-a", action: "archive" }, { id: "active-b", action: "archive" }]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
  });
}

test("partial archive retains only failed selection and its error across refresh, then retries", async ({ page }) => {
  const state = await fixture(page);
  state.failures.add("active-b");
  const active = page.getByRole("region", { name: "Active threads", exact: true });
  await active.getByRole("checkbox", { name: "Select all active Worker Threads" }).check();
  await active.getByRole("button", { name: "Archive selected", exact: true }).click();
  await expect(active).toContainText("1 selected");
  await expect(active.getByRole("checkbox", { name: "Select active-b · #active-b", exact: true })).toBeChecked();
  await expect(page.getByRole("alert")).toContainText("active-b · #active-b: Thread is busy");
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Thread is busy");
  state.failures.clear();
  await active.getByRole("button", { name: "Archive selected", exact: true }).click();
  await expect(active.getByRole("link")).toHaveCount(1);
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(state.calls.map((call) => call.id)).toEqual(["active-a", "active-b", "active-b"]);
});

for (const action of ["archive", "delete"]) {
  test(`batch ${action} completes selected children before their parents`, async ({ page }) => {
    const state = await fixture(page, { parentChild: true });
    const section = page.getByRole("region", { name: action === "archive" ? "Active threads" : "Archived threads", exact: true });
    await section.getByRole("checkbox", { name: action === "archive" ? "Select all active Worker Threads" : "Select all archived Worker Threads" }).check();
    if (action === "delete") page.once("dialog", (dialog) => dialog.accept());
    await section.getByRole("button", { name: action === "archive" ? "Archive selected" : "Delete selected", exact: true }).click();
    await expect(section.getByRole("link")).toHaveCount(action === "archive" ? 1 : 0);
    await expect(page.getByRole("alert")).toHaveCount(0);
    expect(state.calls.map((call) => call.id)).toEqual(action === "archive" ? ["active-b", "active-a"] : ["old-b", "old-a"]);
  });
}

test("later ancestry groups keep their original Agent when navigation changes during a batch", async ({ page }) => {
  const state = await fixture(page, { parentChild: true });
  let release;
  state.holdMutation = new Promise((resolve) => { release = resolve; });
  await page.getByRole("checkbox", { name: "Select all active Worker Threads" }).check();
  await page.getByRole("button", { name: "Archive selected", exact: true }).click();
  await expect.poll(() => state.calls.length).toBe(1);
  await page.getByRole("link", { name: /^Open agent-b,/ }).click();
  await expect(page).toHaveURL(/\/agents\/agent-b\/threads$/);
  release();
  await expect.poll(() => state.calls.length).toBe(2);
  expect(state.paths.every((path) => path.startsWith("/agents/agent-a/"))).toBe(true);
});

test("batch delete names selected Threads, supports cancel and preserves partial failures for retry", async ({ page }) => {
  const state = await fixture(page);
  const archived = page.getByRole("region", { name: "Archived threads", exact: true });
  await archived.getByRole("checkbox", { name: "Select all archived Worker Threads" }).check();
  page.once("dialog", (dialog) => {
    expect(dialog.message()).toContain("old-a · #old-a");
    expect(dialog.message()).toContain("old-b · #old-b");
    return dialog.dismiss();
  });
  await archived.getByRole("button", { name: "Delete selected", exact: true }).click();
  expect(state.calls).toHaveLength(0);
  await expect(archived).toContainText("2 selected");
  state.failures.add("old-b");
  page.once("dialog", (dialog) => dialog.accept());
  await archived.getByRole("button", { name: "Delete selected", exact: true }).click();
  await expect(archived.getByRole("link")).toHaveCount(1);
  await expect(archived.getByRole("checkbox", { name: "Select old-b · #old-b", exact: true })).toBeChecked();
  await expect(page.getByRole("alert")).toContainText("old-b · #old-b: Thread is busy");
  state.failures.clear();
  page.once("dialog", (dialog) => dialog.accept());
  await archived.getByRole("button", { name: "Delete selected", exact: true }).click();
  await expect(archived).toContainText("No archived threads.");
  expect(state.calls).toEqual([{ id: "old-a", action: "delete" }, { id: "old-b", action: "delete" }, { id: "old-b", action: "delete" }]);
});

test("newer snapshots prune changed retention without transferring selection or accepting late refresh", async ({ page }) => {
  const state = await fixture(page);
  const active = page.getByRole("region", { name: "Active threads", exact: true });
  const archived = page.getByRole("region", { name: "Archived threads", exact: true });
  await active.getByRole("checkbox", { name: "Select active-a · #active-a", exact: true }).check();
  await archived.getByRole("checkbox", { name: "Select old-a · #old-a", exact: true }).check();
  let release;
  state.holdList = new Promise((resolve) => { release = resolve; });
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect.poll(() => state.heldListStarted).toBe(true);
  state.active_threads = state.active_threads.filter((thread) => thread.thread_id !== "active-a");
  state.archived_threads.push(item("active-a", true));
  await page.evaluate(() => window.dispatchEvent(new Event("juex:threads-changed")));
  await expect(active.getByRole("button", { name: "Archive selected", exact: true })).toHaveCount(0);
  await expect(archived.getByRole("checkbox", { name: "Select active-a · #active-a", exact: true })).not.toBeChecked();
  await expect(archived.getByRole("checkbox", { name: "Select old-a · #old-a", exact: true })).toBeChecked();
  const late = page.waitForResponse((response) => response.url().endsWith("/api/threads"));
  release();
  await (await late).finished();
  await expect(active.getByRole("link", { name: "active-a · #active-a", exact: true })).toHaveCount(0);
  await page.getByRole("link", { name: /^Open agent-b,/ }).click();
  await expect(page).toHaveURL(/\/agents\/agent-b\/threads$/);
  await expect(archived.getByRole("checkbox", { name: "Select old-a · #old-a", exact: true })).not.toBeChecked();
  await expect(archived.getByRole("button", { name: "Delete selected", exact: true })).toHaveCount(0);
});

test("stopped Agent keeps Thread checkboxes disabled", async ({ page }) => {
  await fixture(page, { stopped: true });
  const active = page.getByRole("region", { name: "Active threads", exact: true });
  await expect(active.getByRole("checkbox", { name: "Select all active Worker Threads" })).toBeDisabled();
  await expect(active.getByRole("checkbox", { name: "Select active-a · #active-a", exact: true })).toBeDisabled();
});
