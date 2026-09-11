import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function selectScratchpad(page) {
  await page.getByRole("combobox", { name: "File root" }).click();
  await page.getByRole("option", { name: "Scratchpad", exact: true }).click();
}

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
    if (path.endsWith("/recitation")) return options.recitation ? options.recitation(route) : json(null);
    if (path.endsWith("/status")) return json({ cursor: "cursor-1", thread: { id: threadID, alias: "main", state: "idle", working: false, pending_count: 0, max_pending_inputs: 8, can_accept_input: true }, tools: [], token_usage: { input_tokens: 0, output_tokens: 0 } });
    if (/\/threads\/[^/]+$/.test(path)) return json({ thread_id: threadID, alias: "main", dir: `/tmp/module-browser/${threadID}`, retention_state: options.readOnly ? "archived" : "active", execution_state: "idle", created_at: "2026-09-07T00:00:00Z", last_activity_at: "2026-09-07T00:00:00Z", revision: 1, generation_id: "g1", turn_count: 0, pending_input_count: 0, items: options.items ?? [], has_more_before: false, event_cursor: "cursor-1" });
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
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
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
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
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
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
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
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "File root" })).toBeVisible();
  await page.getByRole("tab", { name: "Status", exact: true }).click();
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
  await page.getByRole("tab", { name: "Status", exact: true }).click();
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
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
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
  await expect(page.getByRole("combobox", { name: "File root" })).toHaveText("Workspace");
  expect(reads()).toBe(beforeLate);
  await page.unroute("**/resources/files/content?*");
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  expect(reads()).toBeGreaterThan(beforeLate);
});

test("preview and unrelated snapshots preserve the selected root subscription", async ({ page }) => {
  const reads = await openModuleThread(page);
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  const beforeUpdate = reads();
  await publishModules(page);
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  expect(reads()).toBe(beforeUpdate);
  expect(await page.evaluate(() => window.fileSources.length)).toBe(1);
  await publishModules(page, { composition: "restarted" });
  await expect(page.getByText("Scoped file preview", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "File root" })).toHaveText("Workspace");
});

test("unsupported and broken renderers leave other contributions usable", async ({ page }) => {
  await openModuleThread(page);
  await publishModules(page, { unknown: true, version: true });
  await expect(page.getByText("future.status v1 unavailable: unsupported contribution")).toBeVisible();
  await expect(page.getByText("goal.status v2 unavailable: unsupported contribution")).toBeVisible();
  await page.getByRole("tab", { name: "Status", exact: true }).click();
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await publishModules(page, { broken: true });
  await expect(page.getByText("Notes unavailable: display error")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
  await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
  await publishModules(page);
  await page.getByRole("tab", { name: "Status", exact: true }).click();
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await page.getByRole("button", { name: /^Open notes:/ }).click();
  await publishModules(page, { notes: "Updated notes stay open" });
  await expect(page.getByText("Updated notes stay open", { exact: true })).toBeVisible();
});

for (const mode of ["readOnly", "stopped"]) {
  test(`${mode} Threads retain readable module slots`, async ({ page }) => {
    await openModuleThread(page, "ready", { [mode]: true });
    await page.getByRole("button", { name: /^Open goal:/ }).click();
    await expect(page.locator("details[open]").getByText("Read only", { exact: true })).toBeVisible();
    await expect(page.getByText("Verify module views", { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
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
    await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
    await page.getByRole("button", { name: "draft.md", exact: true }).click();
    await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
    await page.evaluate((route) => {
      window.history.pushState({}, "", route);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, route);
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "File root" })).toHaveText("Workspace");
    await expect(page.getByText("Scoped file preview", { exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => window.fileSources.every((source) => source.readyState === EventSource.CLOSED))).toBe(true);
    await page.evaluate(() => {
      window.fileSources.forEach((source) => source.changed());
      window.moduleSources.filter((source) => source.readyState === EventSource.CLOSED).forEach((source) => source.send({ ...window.moduleBaseline, revision: "late", ui: [], modules: {} }));
    });
    await page.getByRole("tab", { name: "Status", exact: true }).click();
    await expect(page.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
    const treeResponse = page.waitForResponse((response) => response.url().includes(`${route.replace("/threads/", "/api/threads/")}/modules/scratchpad/resources/files/tree`));
    await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
    await treeResponse;
    await expect(page.getByRole("button", { name: "draft.md", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.moduleSources.filter((source) => source.readyState !== EventSource.CLOSED).length)).toBe(1);
  });
}

test("mobile file sheet follows module removal without retaining its preview", async ({ page }) => {
  await openModuleThread(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Open sidebar", exact: true }).click();
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
  await page.getByRole("button", { name: "draft.md", exact: true }).click();
  await expect(page.getByText("Scoped file preview", { exact: true })).toBeVisible();
  await publishModules(page, { only: ["notes"] });
  await expect(page.getByText("Scoped file preview", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Refresh workspace", exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Status", exact: true }).click();
  await expect(page.getByRole("button", { name: /^Open notes:/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Open goal:/ })).toHaveCount(0);
});

test("read-only transitions stop file subscriptions while manual refresh remains available", async ({ page }) => {
  const reads = await openModuleThread(page);
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await selectScratchpad(page);
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

test("Thread state stays in the sidebar and the Agent title opens Chat", async ({ page }) => {
  await openModuleThread(page);
  const sidebar = page.getByRole("complementary", { name: "Thread sidebar" });
  await expect(sidebar.getByRole("button", { name: /^Open goal:/ })).toBeVisible();
  await expect(page.locator("header").getByRole("link", { name: "Chat with test-agent" })).toHaveAttribute("href", "/agents/test-agent");
  await expect(page.locator("header").getByRole("link", { name: "Runtime", exact: true })).toHaveAttribute("href", "/agents/test-agent/runtime");
  await expect(page.getByRole("tab", { name: "Status", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("button", { name: "Close sidebar", exact: true }).click();
  await expect(sidebar).toHaveCount(0);
  const entry = page.getByRole("button", { name: "Open sidebar", exact: true });
  await expect(entry).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(sidebar).toBeVisible();
  await expect(sidebar.getByRole("button", { name: "Close sidebar", exact: true })).toBeFocused();
});

for (const viewport of [{ width: 820, height: 1180 }, { width: 1180, height: 820 }, { width: 390, height: 844 }, { width: 320, height: 720 }]) {
  test(`sidebar drawer preserves the composer at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await openModuleThread(page);
    await page.setViewportSize(viewport);
    const composer = page.getByPlaceholder("Ask juex anything...");
    await composer.fill("Keep my draft");
    const entry = page.getByRole("button", { name: "Open sidebar", exact: true });
    await entry.click();
    const drawer = page.getByRole("dialog", { name: "Thread sidebar", exact: true });
    await expect(drawer).toBeVisible();
    await drawer.getByRole("button", { name: /^Open notes:/ }).click();
    await expect(drawer.getByText("Check scoped resources", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const bounds = await drawer.boundingBox();
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width + 1);
    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(entry).toBeFocused();
    await expect(composer).toHaveValue("Keep my draft");
    await expect(page.locator("header").getByRole("link", { name: "Runtime", exact: true })).toBeVisible();
  });
}

const recordedRecitation = (text) => ({ epoch_id: text, turn_id: "turn", recorded_at: "2026-09-09T04:00:00Z", generation_id: "g1", iter: 0,
  fragments: [{ message_id: "runtime-notes", text: `## Notes\n${text}` }] });

test("Recitation is scoped to visible Status and rejects responses from a previous open", async ({ page }) => {
  const pending = [];
  await openModuleThread(page, "ready", { recitation: (route) => { pending.push(route); } });
  await expect.poll(() => pending.length).toBeGreaterThan(0);
  await page.getByRole("button", { name: "Close sidebar", exact: true }).click();
  const old = pending.splice(0);
  await page.getByRole("button", { name: "Open sidebar", exact: true }).click();
  await expect.poll(() => pending.length).toBeGreaterThan(0);
  await pending.at(-1).fulfill({ json: recordedRecitation("Current recorded text") });
  await page.getByRole("button", { name: /^Open recitation:/ }).click();
  await page.locator("summary").filter({ hasText: /^Notes$/ }).click();
  await expect(page.getByText("Current recorded text", { exact: false })).toBeVisible();
  for (const route of old) await route.fulfill({ json: recordedRecitation("Obsolete recorded text") }).catch(() => {});
  await expect(page.getByText("Obsolete recorded text", { exact: false })).toHaveCount(0);
  const count = pending.length;
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await publishModules(page, { only: ["notes"] });
  await expect(page.getByRole("button", { name: "Refresh workspace", exact: true })).toBeVisible();
  expect(pending.length).toBe(count);
});

test("Recitation keeps errors distinct from an empty recorded request and recovers", async ({ page }) => {
  let failed = true;
  await openModuleThread(page, "ready", { recitation: (route) => failed
    ? route.fulfill({ status: 500, json: { error: { message: "Recorded context unreadable" } } })
    : route.fulfill({ json: { ...recordedRecitation("empty"), fragments: [] } }) });
  await page.getByRole("button", { name: "Open recitation: Unavailable" }).click();
  await expect(page.getByText("Recorded context unreadable", { exact: true })).toBeVisible();
  failed = false;
  await page.getByRole("button", { name: "Refresh Recitation" }).click();
  await expect(page.getByText("This request contains no Recitation fragments.", { exact: true })).toBeVisible();
  await expect(page.getByText("Recorded context unreadable", { exact: true })).toHaveCount(0);
});

for (const outcome of ["success", "failure"]) {
  test(`Recitation ignores an old ${outcome} after Agent A to B to A`, async ({ page }) => {
    const pending = [];
    await openModuleThread(page, "ready", { recitation: (route) => { pending.push(route); } });
    await expect.poll(() => pending.length).toBeGreaterThan(0);
    const old = pending.splice(0);
    for (const agent of ["other-agent", "test-agent"]) {
      await page.evaluate((agent) => { window.history.pushState({}, "", `/agents/${agent}/threads/0`); window.dispatchEvent(new PopStateEvent("popstate")); }, agent);
      await expect(page.locator("header").getByRole("link", { name: `Chat with ${agent}` })).toBeVisible();
      await expect.poll(() => pending.some((route) => route.request().url().includes(`/agents/${agent}/`))).toBe(true);
    }
    const current = pending.filter((route) => route.request().url().includes("/agents/test-agent/")).at(-1);
    await current.fulfill({ json: recordedRecitation("Newest scope") });
    for (const route of old) await route.fulfill(outcome === "success" ? { json: recordedRecitation("Obsolete scope") } : { status: 500, json: { error: { message: "Obsolete failure" } } }).catch(() => {});
    await page.getByRole("button", { name: "Open recitation: 1 fragments · latest request" }).click();
    await page.locator("summary").filter({ hasText: /^Notes$/ }).click();
    await expect(page.getByText("Newest scope", { exact: false })).toBeVisible();
    await expect(page.getByText(/Obsolete/)).toHaveCount(0);
  });
}

for (const width of [1440, 820, 390, 320]) {
  test(`sidebar controls belong to the content while navigation keeps responsive labels at ${width}px`, async ({ page }) => {
    await openModuleThread(page);
    await page.setViewportSize({ width, height: 900 });
    const header = page.locator('header');
    const runtime = header.getByRole('link', { name: 'Runtime', exact: true });
    const threads = header.getByRole('link', { name: 'Thread Explorer', exact: true });
    for (const [link, label] of [[runtime, 'Runtime'], [threads, 'Threads']]) {
      await expect(link).toBeVisible();
      if (width >= 640) await expect(link.getByText(label, { exact: true })).toBeVisible();
      else await expect(link.getByText(label, { exact: true })).toBeHidden();
    }
    const entry = page.getByRole('button', { name: 'Open sidebar', exact: true });
    const close = page.getByRole('button', { name: 'Close sidebar', exact: true });
    if (width >= 1280) await close.click();
    await expect(entry).toBeVisible();
    const entryBounds = await entry.boundingBox();
    const headerBounds = await header.boundingBox();
    expect(entryBounds.y).toBeGreaterThanOrEqual(headerBounds.y + headerBounds.height);
    expect(entryBounds.y).toBeLessThan(headerBounds.y + headerBounds.height + 16);
    expect(entryBounds.x + entryBounds.width).toBeGreaterThanOrEqual(width - 16);
    expect(entryBounds.width).toBeGreaterThanOrEqual(44);
    const conversation = await page.getByRole('log').boundingBox();
    expect(conversation.x + conversation.width).toBeLessThanOrEqual(entryBounds.x);
    await entry.click();
    const panel = page.locator('#thread-sidebar');
    const panelClose = panel.getByRole('button', { name: 'Close sidebar', exact: true });
    await expect(panelClose).toBeVisible();
    // Measure the settled drawer, not different frames of its entrance animation.
    await panel.evaluate((element) => Promise.all(element.getAnimations().map((animation) => animation.finished)));
    await expect(entry).toBeHidden();
    const closeBounds = await panelClose.boundingBox();
    const panelBounds = await panel.boundingBox();
    const statusBounds = await panel.getByRole('tab', { name: 'Status', exact: true }).boundingBox();
    expect(closeBounds.x - panelBounds.x).toBeLessThanOrEqual(16);
    expect(closeBounds.y - panelBounds.y).toBeLessThanOrEqual(12);
    expect(closeBounds.x + closeBounds.width).toBeLessThanOrEqual(statusBounds.x);
    await panelClose.click();
    await expect(entry).toBeFocused();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await expect(header.getByText('main · #0', { exact: true })).toBeVisible();
    await expect(header.getByLabel('Current Thread status')).toBeVisible();
    await runtime.click();
    await expect(entry).toBeHidden();
    await expect(close).toBeHidden();
    await expect(threads).toBeVisible();
    await expect(threads).toHaveAttribute('href', '/agents/test-agent/threads');
    await threads.click();
    await expect(page).toHaveURL(/\/agents\/test-agent\/threads$/);
    await expect(threads).toHaveAttribute('aria-current', 'page');
    await entry.click();
    await expect(page.getByRole('tab', { name: 'Files', exact: true })).toBeVisible();
    await close.click();
    await header.getByRole('link', { name: 'Chat with test-agent' }).click();
    await expect(page).toHaveURL(/\/agents\/test-agent\/threads\/0$/);
  });
}

for (const width of [1440, 390]) {
  test(`sidebar opener stays above the scrolling conversation at ${width}px`, async ({ page }) => {
    await openModuleThread(page, 'ready', { items: Array.from({ length: 30 }, (_, index) => ({
      type: 'message', seq: index + 1, at: '2026-09-07T00:00:00Z',
      message: { id: `message-${index}`, role: index % 2 ? 'assistant' : 'user',
        blocks: [{ type: 'text', text: `Message ${index}. ` + 'Conversation content remains readable. '.repeat(10) }] },
    })) });
    await page.setViewportSize({ width, height: 900 });
    if (width >= 1280) await page.getByRole('button', { name: 'Close sidebar', exact: true }).click();
    const entry = page.getByRole('button', { name: 'Open sidebar', exact: true });
    const bounds = await entry.boundingBox();
    const scroll = page.getByRole('log').locator('.overflow-y-auto').first();
    await expect.poll(() => scroll.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
    await scroll.evaluate((element) => { element.scrollTop = 0; });
    await expect(page.getByRole('button', { name: 'Scroll to latest message' })).toBeVisible();
    expect(await entry.boundingBox()).toEqual(bounds);
    await entry.click();
    await expect(page.locator('#thread-sidebar')).toBeVisible();
  });
}

test('file root is a single keyboard selector with selection and focus restoration', async ({ page }) => {
  await openModuleThread(page);
  await page.getByRole('tab', { name: 'Files', exact: true }).click();
  const root = page.getByRole('combobox', { name: 'File root' });
  await expect(root).toContainText('Workspace');
  const toolbar = root.locator('..');
  await expect(toolbar.getByText('Workspace', { exact: true })).toHaveCount(1);
  await root.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('option', { name: 'Workspace', exact: true })).toHaveAttribute('data-state', 'checked');
  await expect(page.getByRole('option', { name: 'Workspace', exact: true })).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('option', { name: 'Scratchpad', exact: true })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(root).toContainText('Scratchpad');
  await expect(root).toBeFocused();
  await expect(page.getByRole('button', { name: 'draft.md', exact: true })).toBeVisible();
  await root.click();
  await page.keyboard.press('Escape');
  await expect(root).toBeFocused();
});
