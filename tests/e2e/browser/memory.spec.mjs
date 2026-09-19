import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function fixture(page, options = {}) {
  const original = { id: "release-notes", revision: 1, name: "Release notes", summary: "Concise releases", type: "reference", body: "Original text", scope: { workspace: "/project" }, sources: [{ fleet_id: "fleet", agent_id: "writer", thread_id: "0", generation_id: "g000001", from: 1, through: 1 }], created_at: "2026-09-19T00:00:00Z", updated_at: "2026-09-19T00:00:00Z" };
  let entry = structuredClone(original);
  const calls = [], receipts = new Map();
  await page.addInitScript(() => { window.EventSource = class extends EventTarget { close() {} }; });
  await page.route("**/api/**", async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json([]);
    if (options.offline) return json({ error: { message: "Memory service is offline" } }, 503);
    if (path === "/api/memory/status") return json({ strategy: "basic", entries: entry ? 1 : 0, pending: 0, running: 0, index_ready: true });
    if (path === "/api/memory/entries") {
      const nextPage = Number(url.searchParams.get("offset")) > 0;
      return json({ entries: !options.empty && entry && !url.searchParams.get("q")?.includes("missing") ? [nextPage ? { ...entry, name: "Second page entry" } : entry] : [], next: options.paginated && !nextPage ? 20 : -1, fence: 0 });
    }
    if (path === "/api/memory/entries/release-notes") {
      if (request.method() === "GET") return entry ? json(entry) : json({ error: { message: "Entry missing" } }, 404);
      const body = request.postDataJSON(); calls.push({ method: request.method(), body });
      if (options.conflict) return json({ error: { message: "memory revision conflict: release-notes" } }, 409);
      if (!receipts.has(body.key)) {
        if (request.method() === "DELETE") entry = null;
        else entry = { ...body.entry, revision: body.expected_revision + 1 };
        receipts.set(body.key, { id: body.key, state: "applied", committed: true, index_ready: !options.indexPending, entry_ids: [original.id] });
      }
      if (options.loseFirst && calls.length === 1) return route.abort("failed");
      return json(receipts.get(body.key));
    }
    return json({}, 404);
  });
  return { calls, original };
}

test("Memory edit preserves provenance and retries a lost commit with the identical request", async ({ page }) => {
  const { calls, original } = await fixture(page, { loseFirst: true, indexPending: true });
  await page.goto("/memory");
  await page.getByRole("link", { name: "Release notes", exact: true }).click();
  await expect(page.getByText("Original text", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByLabel("Body", { exact: true }).fill("Corrected text");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.getByLabel("Body", { exact: true })).toHaveValue("Corrected text");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Corrected text", { exact: true })).toBeVisible();
  await expect(page.getByRole("status")).toContainText("index");
  expect(calls).toHaveLength(2);
  expect(calls[0].body).toEqual(calls[1].body);
  expect(calls[1].body.entry.sources).toEqual(original.sources);
  expect(calls[1].body.entry.scope).toEqual(original.scope);
  expect(calls[1].body.expected_revision).toBe(1);
});

test("Memory pagination follows the server and search returns to the first page", async ({ page }) => {
  await fixture(page, { paginated: true });
  await page.goto("/memory");
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await expect(page.getByRole("link", { name: "Second page entry", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Next", exact: true })).toBeDisabled();
  await page.getByLabel("Search memories", { exact: true }).fill("concise");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page).toHaveURL(/\/memory\?q=concise$/);
  await expect(page.getByRole("link", { name: "Release notes", exact: true })).toBeVisible();
});

test("Memory conflicts keep drafts and deletion requires explicit confirmation", async ({ page }) => {
  const options = { conflict: true };
  const { calls } = await fixture(page, options);
  await page.goto("/memory/release-notes");
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByLabel("Body", { exact: true }).fill("My draft");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("alert")).toContainText("conflict");
  await expect(page.getByLabel("Body", { exact: true })).toHaveValue("My draft");
  await page.getByRole("button", { name: "Cancel editing" }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("Release notes");
  await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(calls).toHaveLength(1);
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await dialog.getByRole("button", { name: "Delete memory", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("conflict");
  options.conflict = false;
  await dialog.getByRole("button", { name: "Delete memory", exact: true }).click();
  await expect(page).toHaveURL(/\/memory$/);
  await expect(page.getByText("No memories yet.", { exact: true })).toBeVisible();
});

test("Memory is available without Agents, shows service failures and fits mobile", async ({ page }) => {
  const options = { offline: true };
  await fixture(page, options);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/memory");
  await expect(page.getByRole("alert")).toContainText("offline");
  options.offline = false;
  await page.getByRole("button", { name: "Refresh memories" }).click();
  await expect(page.getByRole("link", { name: "Release notes", exact: true })).toBeVisible();
  await page.getByLabel("Search memories", { exact: true }).fill("missing");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.getByText("No matching memories.", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Open fleet agents", exact: true }).click();
  await page.getByRole("dialog").getByRole("link", { name: "Memory", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
