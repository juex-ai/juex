import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

for (const width of [1440, 390]) test(`retained input evidence recovers after module switches and read errors at ${width}px`, async ({ page }) => {
  let mode = "enabled";
  let checked = false;
  const requested = [];
  const marker = { input_id: "input-old", message_id: "msg-old", scope_id: "g1" };
  const row = { thread_id: "0", alias: "main", retention_state: "active", execution_state: "idle", created_at: "2026-09-13T00:00:00Z", last_activity_at: "2026-09-13T00:00:00Z", token_usage: { total: {}, by_model: {} }, turn_count: 1 };
  await page.addInitScript(() => {
    window.sources = [];
    window.EventSource = class extends EventTarget {
      static OPEN = 1; static CLOSED = 2; static CONNECTING = 0;
      readyState = 1;
      constructor(url) { super(); this.url = String(url); window.sources.push(this); }
      close() { this.readyState = 2; }
    };
  });
  await page.route("**/api/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const json = body => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json([{ id: "test-agent", name: "test-agent", enabled: true, workspace: "/tmp/input-status", runtime_health: "healthy" }]);
    if (path.endsWith("/status")) return json({ thread: { id: "0", state: "idle", working: false, pending_count: 0, can_accept_input: true }, tools: [], token_usage: {} });
    if (path.endsWith("/recitation")) return json(null);
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/threads/0")) {
      const older = url.searchParams.has("before");
      const ids = url.searchParams.getAll("input_message_id");
      requested.push(ids);
      const input_tracking = mode === "disabled" ? undefined : { scope_id: "g2", messages: mode === "error" ? {} : older || ids.includes(marker.message_id)
        ? { [marker.message_id]: { ...marker, ...(checked ? { checked_at: "2026-09-13T01:00:00Z", check_message_id: "answer", tool_use_id: "check" } : {}) } } : {},
        ...(mode === "error" ? { error: "Input status is unavailable" } : {}) };
      return json({ ...row, dir: "/tmp/input-status/0", revision: 1, generation_id: "g2", event_cursor: "cursor-1", input_tracking,
        items: [{ type: "message", message: { id: older ? "msg-old" : "msg-new", role: "user", blocks: [{ type: "text", text: older ? "Original older input" : "Newest untracked input" }] } }],
        has_more_before: !older, previous_cursor: older ? undefined : "before-old" });
    }
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.setViewportSize({ width, height: 900 });
  await page.goto("/agents/test-agent/threads/0");
  await page.getByRole("button", { name: "Load older messages" }).click();
  await expect(page.getByRole("button", { name: "Tracking ended", exact: true })).toBeVisible();
  const composer = page.getByRole("textbox", { name: "" });
  await composer.fill("Draft survives revalidation");
  const reconnect = () => page.evaluate(() => window.sources.find(source => source.readyState === 1 && /\/threads\/0\/events/.test(source.url)).dispatchEvent(new Event("open")));
  mode = "disabled";
  await reconnect();
  await expect(page.getByRole("button", { name: "Tracking ended", exact: true })).toHaveCount(0);
  mode = "error";
  await reconnect();
  await expect(page.getByText("Input status is unavailable", { exact: true })).toBeVisible();
  mode = "enabled"; checked = true;
  await reconnect();
  await expect(page.getByRole("button", { name: "Checked by Agent", exact: true })).toBeVisible();
  await expect(page.getByText("Input status is unavailable", { exact: true })).toHaveCount(0);
  await expect(page.getByText("Original older input", { exact: true })).toBeVisible();
  await expect(composer).toHaveValue("Draft survives revalidation");
  expect(requested.filter(ids => ids.includes(marker.message_id)).length).toBeGreaterThanOrEqual(3);
  await page.getByRole("button", { name: "Checked by Agent", exact: true }).click();
  await expect(page.getByText("input-old", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Checked by Agent", exact: true })).toBeFocused();
});
