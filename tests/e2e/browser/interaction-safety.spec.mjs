import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

const row = (id) => ({ thread_id: id, alias: id === "0" ? "main" : "worker", retention_state: "active", execution_state: "idle", created_at: "2026-09-12T00:00:00Z", last_activity_at: "2026-09-12T00:00:00Z", token_usage: { total: { input_tokens: 0, output_tokens: 0 }, by_model: {} }, pending_input_count: 0, current_context_tokens: 0, turn_count: 0, generation_count: 1, thread_revision: 1 });
async function fixture(page, options = {}) {
  const calls = [];
  const agents = ["extensior", "minima"].map((id) => ({ id, name: id, enabled: true, autostart: false, workspace: `/tmp/${id}`, binding: "bound", runtime_health: options.stopped ? "stopped" : "healthy", runtime_present: true, activity: { state: "working", pending_input_count: 2 } }));
  await page.addInitScript(() => {
    window.sources = [];
    window.EventSource = class extends EventTarget {
      static OPEN = 1; static CLOSED = 2; static CONNECTING = 0;
      constructor(url) { super(); this.url = String(url); window.sources.push(this); }
      readyState = 1; close() { this.readyState = 2; }
    };
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (request.method() !== "GET") {
      calls.push({ path, body: request.postDataJSON() });
      if (options.hold) await options.hold;
      if (options.fail) return json({ message: "Test request failed" }, 500);
      if (path.endsWith("/inputs")) return json({ input_id: "accepted-1", turn_id: "turn-1" });
      return json(agents.find((agent) => path.includes(agent.id)));
    }
    if (path === "/api/agents") {
      if (options.rosterHold) await options.rosterHold;
      return json(options.empty ? [] : agents);
    }
    if (path.endsWith("/threads")) return json({ active_threads: [row("0"), row("child")], archived_threads: [] });
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    const id = path.match(/\/threads\/([^/]+)/)?.[1];
    if (path.endsWith("/status") && id) return json({ thread: { id, state: "turn_active", working: true, pending_count: 0, can_accept_input: true }, tools: [], token_usage: {} });
    if (/\/threads\/[^/]+$/.test(path)) return json({ ...row(id), id, dir: `/tmp/${id}`, revision: 1, generation_id: "g1", items: [], has_more_before: false, event_cursor: "cursor-1" });
    return route.fulfill({ status: 404, body: "not found" });
  });
  return calls;
}
const composer = (page) => page.getByRole("textbox", { name: "" });
async function main(page, name = "minima") { await page.getByRole("link", { name: `Chat with ${name}`, exact: true }).click(); }
async function threads(page) { await page.getByRole("link", { name: "Thread Explorer", exact: true }).click(); }

test("text drafts survive navigation and remain isolated by Agent and Thread", async ({ page }) => {
  await fixture(page);
  await page.goto("/agents/minima/threads/0");
  await composer(page).fill("minima main draft");
  await threads(page);
  await page.getByRole("link", { name: "worker · #child", exact: true }).click();
  await expect(composer(page)).toHaveValue("");
  await composer(page).fill("worker draft");
  await main(page);
  await expect(composer(page)).toHaveValue("minima main draft");
  await page.getByRole("link", { name: /^Open extensior, / }).click();
  await expect(composer(page)).toHaveValue("");
  await composer(page).fill("extensior draft");
  await page.getByRole("link", { name: /^Open minima, / }).click();
  await expect(composer(page)).toHaveValue("minima main draft");
  await threads(page);
  await page.getByRole("link", { name: "worker · #child", exact: true }).click();
  await expect(composer(page)).toHaveValue("worker draft");
});

for (const fail of [false, true]) test(`accepted draft clears and failed draft stays after navigation: failure=${fail}`, async ({ page }) => {
  let release;
  const calls = await fixture(page, { fail, hold: new Promise((resolve) => { release = resolve; }) });
  await page.goto("/agents/minima/threads/0");
  await composer(page).fill("submitted draft");
  await page.getByRole("button", { name: "Queue message", exact: true }).click();
  await expect.poll(() => calls.length).toBe(1);
  await threads(page);
  release();
  await main(page);
  await expect(composer(page)).toHaveValue(fail ? "submitted draft" : "");
});

test("accepted submission does not clear text edited while it was pending", async ({ page }) => {
  let release;
  const calls = await fixture(page, { hold: new Promise((resolve) => { release = resolve; }) });
  await page.goto("/agents/minima/threads/0");
  await expect(page.getByRole("button", { name: "Stop current turn", exact: true })).toBeVisible();
  await composer(page).fill("submitted");
  await page.getByRole("button", { name: "Queue message", exact: true }).click();
  await expect.poll(() => calls.length).toBe(1);
  await composer(page).fill("next draft");
  release();
  await threads(page);
  await main(page);
  await expect(composer(page)).toHaveValue("next draft");
});

test("mobile drawer focuses selected navigation and restores the opener", async ({ page }) => {
  const calls = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/agents/minima/threads/0");
  const opener = page.getByRole("button", { name: "Open fleet agents", exact: true });
  await opener.click();
  const drawer = page.getByRole("dialog", { name: "Fleet agents", exact: true });
  await expect(drawer.getByRole("link", { name: /^Open minima, / })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(drawer).not.toBeVisible();
  await expect(opener).toBeFocused();
  await opener.click();
  await page.keyboard.press("Escape");
  await expect(opener).toBeFocused();
  expect(calls).toEqual([]);
});

for (const empty of [false, true]) test(`mobile drawer has safe focus from settings: empty=${empty}`, async ({ page }) => {
  await fixture(page, { empty });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/settings");
  await page.getByRole("button", { name: "Open fleet agents", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Fleet agents", exact: true }).getByRole("link", { name: "juex", exact: true })).toBeFocused();
});

test("mobile lifecycle menu requires confirmation and returns focus safely", async ({ page }) => {
  const calls = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/agents/minima/threads/0");
  await page.getByRole("button", { name: "Open fleet agents", exact: true }).click();
  const drawer = page.getByRole("dialog", { name: "Fleet agents", exact: true });
  const actions = drawer.getByRole("button", { name: "Actions for extensior", exact: true });
  await actions.click();
  await page.getByRole("menuitem", { name: "Stop agent", exact: true }).click();
  const confirmation = page.getByRole("dialog", { name: "Stop extensior?", exact: true });
  await expect(confirmation.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
  await expect(confirmation).toContainText("Working");
  await expect(confirmation).toContainText("2 pending inputs");
  expect(calls).toEqual([]);
  await page.keyboard.press("Escape");
  await expect(actions).toBeFocused();
  await actions.click();
  await page.getByRole("menuitem", { name: "Stop agent", exact: true }).click();
  await confirmation.getByRole("button", { name: "Stop extensior", exact: true }).click();
  await expect.poll(() => calls.length).toBe(1);
  expect(calls[0].path).toBe("/api/agents/extensior/stop");
});

for (const action of ["Stop", "Restart", "Disable"]) test(`Fleet ${action} names target and requires explicit confirmation`, async ({ page }) => {
  const calls = await fixture(page, { fail: true });
  await page.goto("/settings");
  const actions = page.locator("main").getByRole("button", { name: "Actions for minima", exact: true });
  await actions.click();
  await page.getByRole("menuitem", { name: `${action} agent`, exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `${action} minima?`, exact: true });
  await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(calls).toEqual([]);
  await actions.click();
  await page.getByRole("menuitem", { name: `${action} agent`, exact: true }).click();
  await dialog.getByRole("button", { name: `${action} minima`, exact: true }).click();
  await expect.poll(() => calls.length).toBe(1);
  await expect(dialog.getByRole("alert")).toContainText("Test request failed");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(calls[0].path).toBe(`/api/agents/minima/${action.toLowerCase()}`);
});


test("confirmation reflects live work and blocks duplicate lifecycle requests", async ({ page }) => {
  let release;
  const calls = await fixture(page, { hold: new Promise((resolve) => { release = resolve; }) });
  await page.goto("/settings");
  await page.locator("main").getByRole("button", { name: "Actions for minima", exact: true }).click();
  await page.getByRole("menuitem", { name: "Restart agent", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Restart minima?", exact: true });
  await page.evaluate(() => {
    for (const source of window.sources.filter((s) => s.url === "/api/fleet/events" && s.readyState === 1)) {
      source.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "agent.status", agent_id: "minima", activity: { state: "idle", pending_input_count: 0 } }) }));
    }
  });
  await expect(dialog).toContainText("Idle");
  await expect(dialog).toContainText("0 pending inputs");
  await dialog.getByRole("button", { name: "Restart minima", exact: true }).click();
  await expect.poll(() => calls.length).toBe(1);
  await expect(dialog.getByRole("button", { name: "Applying…", exact: true })).toBeDisabled();
  await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeVisible();
  expect(calls).toHaveLength(1);
  release();
  await expect(dialog).not.toBeVisible();
});


test("starting a stopped Agent stays direct and failures remain visible on mobile", async ({ page }) => {
  const calls = await fixture(page, { stopped: true, fail: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/agents/minima/threads/0");
  await page.getByRole("button", { name: "Open fleet agents", exact: true }).click();
  await page.getByRole("dialog", { name: "Fleet agents", exact: true }).getByRole("button", { name: "Actions for minima", exact: true }).click();
  await page.getByRole("menuitem", { name: "Start agent", exact: true }).click();
  await expect.poll(() => calls.length).toBe(1);
  expect(calls[0].path).toBe("/api/agents/minima/start");
  const dialog = page.getByRole("dialog", { name: "Unable to update minima", exact: true });
  await expect(dialog.getByRole("alert")).toContainText("Test request failed");
  await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeFocused();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
});


test("a draft entered before the Agent roster loads keeps its route identity", async ({ page }) => {
  let release;
  await fixture(page, { rosterHold: new Promise((resolve) => { release = resolve; }) });
  await page.goto("/agents/minima/threads/0");
  await composer(page).fill("typed while roster loads");
  release();
  await expect(page.getByRole("link", { name: "Chat with minima", exact: true })).toBeVisible();
  await expect(composer(page)).toHaveValue("typed while roster loads");
  await threads(page);
  await main(page);
  await expect(composer(page)).toHaveValue("typed while roster loads");
});


test("the condensed lifecycle menu stays visible without horizontal scrolling on desktop", async ({ page }) => {
  await fixture(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/settings");
  const table = page.locator("main .overflow-x-auto");
  await expect(page.locator("main").getByRole("button", { name: "Actions for minima", exact: true })).toBeVisible();
  expect(await table.evaluate((element) => element.scrollWidth - element.clientWidth)).toBe(0);
});
