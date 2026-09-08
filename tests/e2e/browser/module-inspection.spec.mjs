import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function openModuleThread(page, mode = "ready", options = {}) {
  let resourceReads = 0;
  const enabled = mode !== "disabled";
  const state = (id, value) => ({ module_id: id, version: 1, revision: id, status: mode === "error" ? "error" : "ready", value,
    resources: [], operations: [], ...(mode === "error" ? { error: "unreadable state" } : {}) });
  const snapshot = { agent_id: "test-agent", thread_id: "0", composition_revision: mode, revision: mode, read_only: Boolean(options.readOnly || options.stopped),
    observed_cursor: { generation_id: "g1", seq: 1, offset: 1 },
    modules: enabled ? { goal: state("goal", mode === "empty" ? null : { description: "Verify module views", status: "in_progress" }),
      notes: state("notes", mode === "empty" ? null : { content: "Check scoped resources" }),
      scratchpad: { ...state("scratchpad", null), resources: ["files"] } } : {},
    ui: enabled ? ["goal.status", "notes.status", "scratchpad.files"].map((id) => ({ id, module_id: id.split(".")[0], version: 1 })) : [],
  };
  if (options.only) {
    snapshot.ui = snapshot.ui.filter((item) => options.only.includes(item.module_id));
    snapshot.modules = Object.fromEntries(Object.entries(snapshot.modules).filter(([id]) => options.only.includes(id)));
  }
  await page.addInitScript((baseline) => {
    const NativeEventSource = window.EventSource;
    window.moduleSources = [];
    window.moduleBaseline = baseline;
    window.fileSources = [];
    class ModuleSource extends EventTarget {
      readyState = NativeEventSource.OPEN;
      onmessage = null;
      onerror = null;
      constructor(url) {
        super();
        this.url = String(url);
        const parts = this.url.match(/\/agents\/([^/]+)\/api\/threads\/([^/]+)/);
        this.baseline = { ...baseline, agent_id: parts[1], thread_id: parts[2] };
        window.moduleSources.push(this);
        queueMicrotask(() => { if (!window.deferModuleBaseline) this.sendBaseline(); });
      }
      send(value) {
        this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(value) }));
      }
      sendBaseline() {
        if (this.readyState === NativeEventSource.CLOSED) return;
        this.readyState = NativeEventSource.OPEN;
        this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(this.baseline) }));
      }
      fail() {
        this.readyState = NativeEventSource.CONNECTING;
        this.onerror?.(new Event("error"));
      }
      close() { this.readyState = NativeEventSource.CLOSED; }
    }
    window.EventSource = class extends NativeEventSource {
      constructor(url, options) {
        if (String(url).endsWith("/modules/events")) return new ModuleSource(url);
        if (String(url).includes("/resources/files/events")) {
          const source = new EventTarget();
          Object.assign(source, { url: String(url), readyState: NativeEventSource.OPEN, onmessage: null,
            close() { this.readyState = NativeEventSource.CLOSED; },
            changed() { this.onmessage?.(new MessageEvent("message", { data: "{}" })); } });
          window.fileSources.push(source);
          return source;
        }
        super(url, options);
      }
    };
  }, snapshot);
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const scope = path.match(/\/agents\/([^/]+)\/api\/threads\/([^/]+)/);
    const agentID = scope?.[1] ?? "test-agent";
    const threadID = scope?.[2] ?? "0";
    if (path.endsWith("/events") || path.endsWith("/resource-events")) return route.abort();
    const json = (body) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json(["test-agent", "other-agent"].map((id) => ({ id, name: id, workspace: "/tmp/module-browser", enabled: true,
      autostart: false, binding: "bound", runtime_health: options.stopped ? "stopped" : "healthy", runtime_present: !options.stopped, process_alive: !options.stopped, endpoint_reachable: !options.stopped, endpoint_matched: !options.stopped })));
    if (path.endsWith("/modules")) { if (options.defer) await options.defer; return json({ ...snapshot, agent_id: agentID, thread_id: threadID }); }
    if (path.endsWith("/resources/files/tree")) { resourceReads++; return json({ name: "scratchpad", path: "/", is_dir: true, children: [{ name: "draft.md", path: "draft.md", is_dir: false }] }); }
    if (path.endsWith("/resources/files/content")) { resourceReads++; return json({ path: "draft.md", content: "Scoped file preview", kind: "text", size: 19, truncated: false }); }
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/context")) return json({ messages: [], estimated_tokens: 0 });
    if (path.endsWith("/status")) return json({ cursor: "cursor-1", thread: { id: threadID, alias: "main", state: "idle", working: false, pending_count: 0, max_pending_inputs: 8, can_accept_input: true }, tools: [], token_usage: { input_tokens: 0, output_tokens: 0 } });
    if (/\/threads\/[^/]+$/.test(path)) return json({ thread_id: threadID, alias: "main", dir: `/tmp/module-browser/${threadID}`, retention_state: options.readOnly ? "archived" : "active", execution_state: "idle", created_at: "2026-09-07T00:00:00Z", last_activity_at: "2026-09-07T00:00:00Z", revision: 1, generation_id: "g1", turn_count: 0, pending_input_count: 0, items: [], has_more_before: false, event_cursor: "cursor-1" });
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/agents/test-agent/threads/0");
  return () => resourceReads;
}

test("module state loads through the shared snapshot and file resources stay lazy", async ({ page }) => {
  const reads = await openModuleThread(page);
  const badge = page.getByRole("button", { name: /^Open goal:/ });
  await expect(badge).toBeVisible();
  await badge.click();
  await expect(page.getByText("Verify module views", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: /^Open notes:/ }).click();
  await expect(page.getByText("Check scoped resources", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  expect(reads()).toBe(0);
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  expect(reads()).toBeGreaterThanOrEqual(2);
});

test("disabled composition hides module controls and does not read resource bodies", async ({ page }) => {
  const reads = await openModuleThread(page, "disabled");
  await expect(page.getByRole("group", { name: "Thread status", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "File root" })).toHaveCount(0);
  expect(reads()).toBe(0);
});

test("empty state and unreadable state have distinct presentation", async ({ page }) => {
  await openModuleThread(page, "empty");
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
  await page.unrouteAll();
  await openModuleThread(page, "error");
  await expect(page.getByText("Goal unavailable: unreadable state", { exact: true })).toBeVisible();
});

test("a directory refresh preserves an in-flight module file preview", async ({ page }) => {
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
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await contentStarted;
  const refreshed = page.waitForResponse("**/resources/files/tree");
  await page.evaluate(() => window.fileSources.find((source) => source.readyState !== EventSource.CLOSED).changed());
  await refreshed;
  releaseContent();
  await expect(page.getByText("Preview survives directory refresh", { exact: true })).toBeVisible();
});

test("module controls share one Thread subscription", async ({ page }) => {
  await openModuleThread(page);
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.moduleSources.filter((source) => source.readyState !== EventSource.CLOSED).length)).toBe(1);
});

test("stream failure marks retained module state unavailable and the same baseline recovers", async ({ page }) => {
  await openModuleThread(page);
  const badge = page.getByRole("button", { name: /^Open goal:/ });
  await expect(badge).toBeVisible();
  await page.evaluate(() => window.moduleSources.find((source) => source.readyState !== EventSource.CLOSED).fail());
  await expect(page.getByText("Module state unavailable", { exact: true })).toBeVisible();
  await expect(badge).toBeVisible();
  await expect(page.getByRole("combobox", { name: "File root" })).toBeVisible();
  await page.evaluate(() => window.moduleSources.find((source) => source.readyState !== EventSource.CLOSED).sendBaseline());
  await expect(badge).toBeVisible();
  await expect(page.getByText("Module state unavailable", { exact: true })).toHaveCount(0);
});

test("a deliberate baseline close stays available but a failed reconnect surfaces the error", async ({ page }) => {
  await openModuleThread(page);
  const badge = page.getByRole("button", { name: /^Open goal:/ });
  await expect(badge).toBeVisible();
  await page.evaluate(() => {
    const source = window.moduleSources.find((item) => item.readyState !== EventSource.CLOSED);
    source.dispatchEvent(new MessageEvent("revalidate", { data: "{}" }));
    source.fail();
  });
  await expect(badge).toBeVisible();
  await expect(page.getByText("Module state unavailable", { exact: true })).toHaveCount(0);
  await page.evaluate(() => window.moduleSources.find((source) => source.readyState !== EventSource.CLOSED).fail());
  await expect(page.getByText("Module state unavailable", { exact: true })).toBeVisible();
});

async function publishModules(page, change = {}) {
  await page.evaluate((change) => {
    const next = structuredClone(window.moduleBaseline);
    next.revision = String(window.moduleRevision = (window.moduleRevision ?? 0) + 1);
    if (change.composition) next.composition_revision = change.composition;
    if (change.readOnly !== undefined) next.read_only = change.readOnly;
    if (change.only) {
      next.ui = next.ui.filter((item) => change.only.includes(item.module_id));
      next.modules = Object.fromEntries(Object.entries(next.modules).filter(([id]) => change.only.includes(id)));
    }
    if (change.unknown) next.ui.push({ id: "future.status", module_id: "future", version: 1 });
    if (change.version) next.ui.find((item) => item.id === "goal.status").version = 2;
    if (change.broken) {
      next.modules.notes.value = { content: {} };
      next.modules.notes.revision = `broken-${next.revision}`;
    }
    if (change.notes) {
      next.modules.notes.value = { content: change.notes };
      next.modules.notes.revision = `notes-${next.revision}`;
    }
    window.moduleSources.find((source) => source.readyState !== EventSource.CLOSED).send(next);
  }, change);
}

test("Goal and Notes independently follow the server contribution list", async ({ page }) => {
  const reads = await openModuleThread(page);
  await publishModules(page, { only: ["goal"] });
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toHaveCount(0);
  await publishModules(page, { only: ["notes"] });
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await publishModules(page, { only: [] });
  await expect(page.getByRole("button", { name: /^Open (goal|notes):/ })).toHaveCount(0);
  expect(reads()).toBe(0);
});

test("disabling a selected root cancels preview and subscription and forgets the selection", async ({ page }) => {
  const reads = await openModuleThread(page);
  let releaseContent;
  const held = new Promise((resolve) => { releaseContent = resolve; });
  let markStarted;
  const started = new Promise((resolve) => { markStarted = resolve; });
  await page.route("**/resources/files/content?*", async (route) => {
    markStarted();
    await held;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ path: "draft.md", content: "Late disabled content", size: 1, truncated: false }) }).catch(() => {});
  });
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await started;
  await publishModules(page, { only: ["goal", "notes"] });
  await expect(page.getByRole("combobox", { name: "File root" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Refresh workspace", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.fileSources.every((source) => source.readyState === EventSource.CLOSED))).toBe(true);
  const beforeLate = reads();
  await page.evaluate(() => window.fileSources.forEach((source) => source.changed()));
  releaseContent();
  await expect(page.getByText("Late disabled content")).toHaveCount(0);
  await publishModules(page);
  await expect(page.getByRole("combobox", { name: "File root" })).toHaveValue("workspace");
  expect(reads()).toBe(beforeLate);
  await page.unroute("**/resources/files/content?*");
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  expect(reads()).toBeGreaterThan(beforeLate);
});

test("preview and unrelated snapshots preserve the selected root subscription", async ({ page }) => {
  const reads = await openModuleThread(page);
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  const beforeUpdate = reads();
  await publishModules(page);
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  expect(reads()).toBe(beforeUpdate);
  expect(await page.evaluate(() => window.fileSources.length)).toBe(1);
  await publishModules(page, { composition: "restarted" });
  await expect(page.getByText("Scoped file preview", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "File root" })).toHaveValue("workspace");
});

test("unsupported and broken renderers leave other contributions usable", async ({ page }) => {
  await openModuleThread(page);
  await publishModules(page, { unknown: true, version: true });
  await expect(page.getByText("future.status v1 unavailable: unsupported contribution")).toBeVisible();
  await expect(page.getByText("goal.status v2 unavailable: unsupported contribution")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await publishModules(page, { broken: true });
  await expect(page.getByText("Notes unavailable: display error")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  await publishModules(page);
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await page.getByRole("button", { name: /^Open notes:/ }).click();
  await publishModules(page, { notes: "Updated notes stay open" });
  await expect(page.getByText("Updated notes stay open", { exact: true })).toBeVisible();
});

for (const mode of ["readOnly", "stopped"]) {
  test(`${mode} Threads retain readable module slots`, async ({ page }) => {
    await openModuleThread(page, "ready", { [mode]: true });
    await page.getByRole("button", { name: /^Open goal:/ }).click();
    await expect(page.getByText("Goal · Read only", { exact: true })).toBeVisible();
    await expect(page.getByText("Verify module views", { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
    await page.getByRole("button", { name: "draft.md", exact: true }).click();
    await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.fileSources.length)).toBe(0);
    await expect(page.getByRole("textbox", { name: /message/i })).toHaveCount(0);
  });
}

test("initial snapshot loading is distinct from disabled and empty modules", async ({ page }) => {
  await page.addInitScript(() => { window.deferModuleBaseline = true; });
  let release;
  const defer = new Promise((resolve) => { release = resolve; });
  await openModuleThread(page, "empty", { defer });
  await expect(page.getByText("Loading modules…", { exact: true })).toBeVisible();
  release();
  await page.getByRole("button", { name: "Open goal: goal none", exact: true }).click();
  await expect(page.getByText("No goal state for this thread.")).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Open notes: notes empty", exact: true }).click();
  await expect(page.getByText("No working notes for this thread.")).toBeVisible();
});

for (const route of ["/agents/test-agent/threads/1", "/agents/other-agent/threads/0"]) {
  test(`module roots reject old callbacks after switching to ${route}`, async ({ page }) => {
    await openModuleThread(page);
    await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
    await page.getByRole("button", { name: "draft.md", exact: true }).click();
    await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
    await page.evaluate((route) => {
      window.history.pushState({}, "", route);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, route);
    await expect(page.getByRole("combobox", { name: "File root" })).toHaveValue("workspace");
    await expect(page.getByText("Scoped file preview", { exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => window.fileSources.every((source) => source.readyState === EventSource.CLOSED))).toBe(true);
    await page.evaluate(() => {
      window.fileSources.forEach((source) => source.changed());
      window.moduleSources.filter((source) => source.readyState === EventSource.CLOSED).forEach((source) => source.send({ ...window.moduleBaseline, revision: "late", ui: [], modules: {} }));
    });
    await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
    const treeResponse = page.waitForResponse((response) => response.url().includes(`${route.replace("/threads/", "/api/threads/")}/modules/scratchpad/resources/files/tree`));
    await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
    await treeResponse;
    await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.moduleSources.filter((source) => source.readyState !== EventSource.CLOSED).length)).toBe(1);
  });
}

test("mobile file sheet follows module removal without retaining its preview", async ({ page }) => {
  await openModuleThread(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Show workspace", exact: true }).click();
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  await publishModules(page, { only: ["notes"] });
  await expect(page.getByText("Scoped file preview", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Refresh workspace", exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toHaveCount(0);
});

test("read-only transitions stop file subscriptions while manual refresh remains available", async ({ page }) => {
  const reads = await openModuleThread(page);
  await page.getByRole("combobox", { name: "File root" }).selectOption("scratchpad.files");
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.fileSources.length)).toBe(1);
  await publishModules(page, { readOnly: true });
  await expect.poll(() => page.evaluate(() => window.fileSources.every((source) => source.readyState === EventSource.CLOSED))).toBe(true);
  const beforeLate = reads();
  await page.evaluate(() => window.fileSources.forEach((source) => source.changed()));
  const refreshed = page.waitForResponse("**/resources/files/tree");
  await page.getByRole("button", { name: "Refresh scratchpad", exact: true }).click();
  await refreshed;
  expect(reads()).toBe(beforeLate + 1);
  await publishModules(page, { readOnly: false });
  await expect.poll(() => page.evaluate(() => window.fileSources.filter((source) => source.readyState !== EventSource.CLOSED).length)).toBe(1);
});
