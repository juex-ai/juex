import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function fixture(page, options = {}) {
  const original = { id: "release-notes", revision: 1, name: "Release notes", summary: "Concise releases", type: "reference", body: "Original text", scope: { workspace: "/project" }, sources: [{ fleet_id: "fleet", agent_id: "writer", thread_id: "0", generation_id: "g000001", from: 1, through: 1 }], created_at: "2026-09-19T00:00:00Z", updated_at: "2026-09-19T00:00:00Z" };
  let entry = structuredClone(original);
  const calls = [], searches = [], receipts = new Map();
  await page.addInitScript(() => { window.EventSource = class extends EventTarget { close() {} }; });
  await page.route("**/api/**", async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return options.rosterUnavailable ? json({ error: { message: "Agent registry unreadable" } }, 503) : json([]);
    if (options.offline) return json({ error: { message: "Memory service is offline" } }, 503);
    if (path === "/api/memory/status") return json({ strategy: "basic", entries: entry ? 1 : 0, pending: 0, running: 0, index_ready: true });
    if (path === "/api/memory/entries") {
      searches.push(url.searchParams.get("q") || "");
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
  return { calls, searches, original };
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

test("Memory remains usable when the initial Agent roster cannot be loaded", async ({ page }) => {
  await fixture(page, { rosterUnavailable: true });
  await page.goto("/memory");
  await expect(page.getByText("Agent registry unreadable", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Release notes", exact: true }).click();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByLabel("Body", { exact: true }).fill("Roster-independent correction");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Roster-independent correction", { exact: true })).toBeVisible();
});

test("Memory keeps the current search when a multilingual query exceeds its budget", async ({ page }) => {
  const { searches } = await fixture(page);
  await page.goto("/memory?q=previous");
  await expect(page.getByRole("link", { name: "Release notes", exact: true })).toBeVisible();
  await page.getByLabel("Search memories", { exact: true }).fill("记".repeat(683));
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Search is too long (2049 bytes; maximum 2048)");
  await expect(page).toHaveURL(/\/memory\?q=previous$/);
  await expect(page.getByLabel("Search memories", { exact: true })).toHaveValue("记".repeat(683));
  expect(searches).toEqual(["previous"]);
  const valid = "记".repeat(682) + "ab";
  await page.getByLabel("Search memories", { exact: true }).fill(valid);
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.getByRole("alert")).not.toBeVisible();
  await expect.poll(() => searches).toEqual(["previous", valid]);
});

test("Memory validates multilingual edit budgets before submitting", async ({ page }) => {
  const { calls } = await fixture(page);
  await page.goto("/memory/release-notes");
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill("记".repeat(43));
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("alert")).toContainText("Name is too long (129 bytes; maximum 128)");
  expect(calls).toHaveLength(0);
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("记".repeat(43));
  await page.getByLabel("Name", { exact: true }).fill("记".repeat(42));
  await page.getByLabel("Summary", { exact: true }).fill("\u{20bb7}".repeat(129));
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("alert")).toContainText("Summary is too long (516 bytes; maximum 512)");
  expect(calls).toHaveLength(0);
  await page.getByLabel("Summary", { exact: true }).fill("\u{20bb7}".repeat(128));
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
  expect(calls).toHaveLength(1);
});

test("Memory validates Body bytes while retaining oversized drafts and allowing empty text", async ({ page }) => {
  const { calls } = await fixture(page);
  await page.goto("/memory/release-notes");
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const body = page.getByLabel("Body", { exact: true });
  for (const [text, bytes] of [["a".repeat(32769), 32769], ["记".repeat(10923), 32769], ["\u{20bb7}".repeat(8193), 32772]]) {
    await body.fill(text);
    await page.getByRole("button", { name: "Save changes" }).click();
    await expect(page.getByRole("alert")).toContainText(`Body is too long (${bytes} bytes; maximum 32768)`);
    await expect(page.getByRole("alert")).toBeInViewport();
    await expect(body).toHaveValue(text);
    expect(calls).toHaveLength(0);
  }
  const boundary = "\u{20bb7}".repeat(8192);
  await body.fill(boundary);
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status")).toContainText("Changes saved");
  expect(calls).toHaveLength(1);
  expect(calls[0].body.entry.body).toBe(boundary);
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await body.fill("");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("No body text.", { exact: true })).toBeVisible();
  expect(calls).toHaveLength(2);
  expect(calls[1].body.entry.body).toBe("");
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
  await page.getByRole("dialog").getByRole("link", { name: "Fleet management", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await page.getByRole("navigation", { name: "Fleet management" }).getByRole("link", { name: "Memory", exact: true }).click();
  await expect(page.getByRole("link", { name: "Release notes", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});


test("Memory distinguishes shared visibility from project applicability", async ({ page }) => {
  await fixture(page);
  await page.goto("/memory");
  await expect(page.getByText("Shared knowledge in this Fleet.", { exact: true })).toBeVisible();
  await expect(page.getByText("reference · Context: /project", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Release notes", exact: true }).click();
  await page.getByText("Sources and metadata", { exact: true }).click();
  await expect(page.getByText("Shared with all Agents in this Fleet. Context describes applicability.", { exact: true })).toBeVisible();
  await expect(page.getByText("Context: /project", { exact: true })).toBeVisible();
  await expect(page.getByText(/Fleet fleet · Agent writer · Thread 0/)).toBeVisible();
});
