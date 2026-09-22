import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function fixture(page) {
  const domains = ["identity", "interpersonal", "knowledge", "health", "projects", "hobbies", "preferences", "finance", "obligations", "temporary", "other"].map(id => ({ id, name: id, description: `${id} knowledge`, policy: "No automatic decay. Evidence is required." }));
  const relation = { predicate: "resides_in", description: "Residence supported by evidence", subjects: ["person"], objects: ["place"], cardinality: "one", competition: ["subject", "project"], temporal: "Effective half-open interval; supersession preserves history.", updates: ["add", "supersede", "correct"], evidence: "Direct original evidence required.", positive: "A completed move ends the prior residence.", negative: "An intention is not a completed move." };
  const source = { fleet_id: "fleet", agent_id: "writer", thread_id: "0", generation_id: "g000001", from: 1, through: 1 };
  const base = { entry_id: "home", revision: 2, scope: { project: "personal" }, subject: { id: "self", name: "User", kind: "person" } };
  const fact = (id, city, lifecycle) => ({ ...base, object: { id: city.toLowerCase(), name: city, kind: "place" }, lifecycle, fact: { id, domain: "identity", subject: "self", predicate: "resides_in", object: city.toLowerCase(), status: lifecycle === "current" ? "valid" : lifecycle, source_type: "user_statement", sources: [source], recorded_at: "2026-01-01T00:00:00Z", valid_from: "2026-01-01T00:00:00Z", reason: "Explicitly reported move", replaces: lifecycle === "current" ? ["old-home"] : [] } });
  const state = { city: "Hangzhou", offline: false, queries: [], paginated: false };
  await page.addInitScript(() => { window.EventSource = class extends EventTarget { close() {} }; });
  await page.route("**/api/**", async route => {
    const url = new URL(route.request().url()), path = url.pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json([]);
    if (state.offline) return json({ error: { message: "Service offline" } }, 503);
    if (path === "/api/memory/status") return json({ entries: 1, strategy: "advanced", pending: 0, running: 0, index_ready: true });
    if (path === "/api/memory/domains") { const id = url.searchParams.get("id"); return json(id ? [{ ...domains.find(d => d.id === id), relations: [relation] }] : domains); }
    if (path === "/api/memory/facts") {
      state.queries.push(Object.fromEntries(url.searchParams));
      const domain = url.searchParams.get("domain"), view = url.searchParams.get("view"), offset = Number(url.searchParams.get("offset") || 0);
      let facts = [fact("current-home", state.city, "current")];
      if (view === "history") facts.push(fact("old-home", "Shanghai", "superseded"));
      if (view === "as_of") facts = [fact("old-home", "Shanghai", "current")];
      if (domain && domain !== "identity") facts = [];
      if (url.searchParams.get("q") === "missing") facts = [];
      if (url.searchParams.get("status")) facts = facts.filter(f => f.lifecycle === url.searchParams.get("status"));
      return json({ facts, total: state.paginated ? 21 : facts.length, domain_total: domain && domain !== "identity" ? 0 : 2, next: state.paginated && !offset ? 20 : -1, fence: 1 });
    }
    if (path === "/api/memory/entries/home") return json({ id: "home", name: "Home history", revision: 2, summary: "Residence audit", body: "Stored residence audit", type: "user", scope: {}, sources: [source], created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" });
    return json({}, 404);
  });
  return state;
}

test("Memory domain structure, real-query navigation, history, provenance and refresh", async ({ page }) => {
  const state = await fixture(page);
  await page.goto("/memory?tab=knowledge");
  await expect(page.getByLabel("Domain", { exact: true }).locator("option")).toHaveCount(12);
  await expect(page.getByRole("region", { name: "Domain structure" })).toContainText("No automatic decay");
  await page.getByRole("region", { name: "Domain structure" }).locator("summary").click();
  await expect(page.getByText("Single value", { exact: true })).toBeVisible();
  await expect(page.getByText("An intention is not a completed move.")).toBeVisible();
  await expect(page.getByRole("list", { name: "Entity relationships" })).toContainText("Hangzhou");
  await expect(page.getByRole("list", { name: "Entity relationships" })).not.toContainText("Shanghai");
  await page.getByRole("button", { name: "Inspect fact current-home" }).click();
  await page.getByText("Fact sources (1)", { exact: true }).click();
  await expect(page.getByRole("region", { name: "Fact details" })).toContainText("Agent writer");
  await expect(page.getByRole("region", { name: "Fact details" })).toContainText("Raw Thread navigation is unavailable");
  await page.getByRole("link", { name: /Open Memory entry home/ }).click();
  await expect(page.getByText("Stored residence audit")).toBeVisible();
  await page.getByRole("link", { name: "All memories" }).click();
  await expect(page.getByRole("region", { name: "Fact details" })).toBeVisible();
  await page.getByLabel("Knowledge view").selectOption("history");
  await page.getByLabel("Lifecycle", { exact: true }).selectOption("superseded");
  await expect(page.getByRole("list", { name: "Entity relationships" })).toContainText("Shanghai");
  await page.getByLabel("Knowledge view").selectOption("as_of");
  await page.getByLabel("Effective time").fill("2025-01-01T00:00:00Z");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect.poll(() => state.queries.at(-1)?.at).toBe("2025-01-01T00:00:00.000Z");
  await page.getByLabel("Knowledge view").selectOption("current");
  await page.getByLabel("Source Agent", { exact: true }).fill("writer");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect.poll(() => state.queries.at(-1)?.source_agent_id).toBe("writer");
  state.city = "Chengdu";
  await page.getByRole("button", { name: "Refresh knowledge" }).click();
  await expect(page.getByRole("list", { name: "Entity relationships" })).toContainText("Chengdu");
  await page.getByRole("button", { name: "User person · self", exact: true }).click();
  await expect(page.getByLabel("Domain", { exact: true })).toHaveValue("");
  await expect(page.getByLabel("Entity ID", { exact: true })).toHaveValue("self");
  await expect.poll(() => state.queries.at(-1)?.entity).toBe("self");
});

test("Memory domain empty, filtered, paged and offline states remain distinct on narrow screens", async ({ page }) => {
  const state = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/memory?tab=knowledge&domain=health");
  await expect(page.getByText("No facts stored in this domain yet.", { exact: false })).toBeVisible();
  await expect(page.getByRole("region", { name: "Domain structure" })).toBeVisible();
  await page.getByLabel("Domain", { exact: true }).selectOption("identity");
  await page.getByLabel("Search facts", { exact: true }).fill("missing");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page.getByText("No facts match these filters.", { exact: false })).toBeVisible();
  state.paginated = true;
  await page.getByRole("button", { name: "Clear filters" }).click();
  await page.getByRole("button", { name: "Next facts" }).click();
  await expect.poll(() => state.queries.at(-1)?.offset).toBe("20");
  await expect(page.getByRole("button", { name: "Next facts" })).toBeDisabled();
  await page.getByRole("button", { name: "Previous facts" }).focus();
  await page.keyboard.press("Enter");
  await expect.poll(() => state.queries.at(-1)?.offset).toBe("0");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
  state.offline = true;
  await page.getByRole("button", { name: "Refresh knowledge" }).click();
  await expect(page.getByRole("alert")).toContainText("This is not an empty result");
});
