import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function fixture(page, { answer = false, activeTurnID = "work-turn" } = {}) {
  let active = !answer;
  const message = (id, blocks, second) => ({ id, role: "assistant", turn_id: "work-turn", model: "test:model", created_at: `2026-09-19T00:00:0${second}Z`, blocks });
  const messages = [
    message("first", [{ type: "tool_use", tool_use_id: "read-call", tool_name: "read", input: { path: "fixture.txt" } }], 0),
    { id: "read-result", role: "user", turn_id: "work-turn", blocks: [{ type: "tool_result", tool_use_id: "read-call", content: "Read fixture" }] },
    message("second", [{ type: "tool_use", tool_use_id: "exec-call", tool_name: "exec_command", input: { command: "pwd" } }], 1),
    { id: "exec-result", role: "user", turn_id: "work-turn", blocks: [{ type: "tool_result", tool_use_id: "exec-call", content: "Command complete" }] },
    message("thinking", [{ type: "reasoning", text: "Verify the result" }], 2),
    ...(answer ? [message("answer", [{ type: "text", text: "Visible final answer" }], 3)] : []),
  ];
  await page.addInitScript(() => {
    window.sources = [];
    window.EventSource = class extends EventTarget {
      static OPEN = 1; static CLOSED = 2; static CONNECTING = 0;
      readyState = 1;
      constructor(url) { super(); this.url = String(url); window.sources.push(this); }
      close() { this.readyState = 2; }
    };
  });
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    const json = body => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json([{ id: "work", name: "Work test", enabled: true, workspace: "/tmp/work", runtime_health: "healthy" }]);
    if (path.endsWith("/status")) return json({ cursor: "cursor-1", thread: { id: "0", state: active ? "turn_active" : "idle", working: active, pending_count: 0, can_accept_input: true }, turn: { id: activeTurnID, state: active ? "active" : "completed" }, tools: [], token_usage: {} });
    if (path.endsWith("/recitation")) return json(null);
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/threads/0")) return json({ thread_id: "0", alias: "main", dir: "/tmp/work/0", retention_state: "active", execution_state: active ? "working" : "idle", revision: 1, generation_id: "g1", event_cursor: "cursor-1", has_more_before: false,
      items: messages.map(({ turn_id, ...message }) => ({ type: "message", turn_id, message })) });
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.goto("/agents/work/threads/0");
  await expect(page.locator("header")).toContainText("main");
  return async () => {
    active = false;
    await page.evaluate(() => window.sources.find(source => source.readyState === 1 && source.url.includes("/threads/0/events")).dispatchEvent(new Event("open")));
  };
}

for (const width of [1440, 390]) test(`tool-only work remains folded after a content-free turn ends at ${width}px`, async ({ page }) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.setViewportSize({ width, height: 900 });
  const complete = await fixture(page);
  const work = page.locator("details.group\\/work-row");
  await expect(work).toHaveCount(1);
  await expect(work.locator(":scope > summary")).toHaveText("Working with tool: exec_command");
  await expect(page.getByText("Verify the result", { exact: true })).not.toBeVisible();
  await work.locator(":scope > summary").click();
  await work.locator("summary").filter({ hasText: /^Thinking$/ }).click();
  await expect(page.getByText("Verify the result", { exact: true })).toBeVisible();
  await complete();
  await expect(work.locator(":scope > summary")).toHaveText("Worked for 2s, called 2 tools");
  await expect(work).toHaveAttribute("open", "");
  await page.reload();
  await expect(work).toHaveCount(1);
  await expect(work.locator(":scope > summary")).toHaveText("Worked for 2s, called 2 tools");
  await expect(work).not.toHaveAttribute("open");
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
  expect(errors).toEqual([]);
});

test("visible content stays outside the completed tool-only work disclosure", async ({ page }) => {
  await fixture(page, { answer: true });
  const work = page.locator("details.group\\/work-row");
  await expect(work).toHaveCount(1);
  await expect(work.locator(":scope > summary")).toHaveText("Worked for 3s, called 2 tools");
  await expect(page.getByText("Visible final answer", { exact: true })).toBeVisible();
  await expect(work.getByText("Visible final answer", { exact: true })).toHaveCount(0);
});

test("a newer active Turn does not revive a persisted tool-only tail", async ({ page }) => {
  await fixture(page, { activeTurnID: "compact-turn" });
  await expect(page.locator("header").getByLabel("Current Thread status")).toHaveText("Working");
  const work = page.locator("details.group\\/work-row");
  await expect(work.locator(":scope > summary")).toHaveText("Worked for 2s, called 2 tools");
});
