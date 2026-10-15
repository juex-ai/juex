import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

async function fixture(page, { rosterUnavailable = false } = {}) {
  const entry = { id: "guide", revision: 1, name: "Fleet guide", summary: "Shared instructions", type: "reference", body: "Guide contents", scope: {}, sources: [] };
  await page.addInitScript(() => { window.EventSource = class extends EventTarget { close() {} }; });
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return rosterUnavailable ? json({ error: { message: "Agent registry unreadable" } }, 503) : json([]);
    if (path === "/api/memory/status") return json({ strategy: "basic", entries: 1, pending: 0, running: 0, index_ready: true });
    if (path === "/api/memory/entries") return json({ entries: [entry], next: -1 });
    if (path === "/api/memory/entries/guide") return json(entry);
    return json({}, 404);
  });
}

const navigation = page => page.getByRole("navigation", { name: "Fleet management" });
const sidebar = page => page.getByRole("complementary", { name: "Fleet agents" });

test("Fleet management groups Settings and Memory across detail routes and history", async ({ page }) => {
  await fixture(page);
  await page.goto("/");
  const entry = sidebar(page).getByRole("link", { name: "Fleet management", exact: true });
  await entry.click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(entry).toHaveAttribute("aria-current", "true");
  await expect(navigation(page).getByRole("link", { name: "Settings", exact: true })).toHaveAttribute("aria-current", "page");
  await navigation(page).getByRole("link", { name: "Memory", exact: true }).click();
  await expect(navigation(page).getByRole("link", { name: "Memory", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(entry).toHaveAttribute("aria-current", "true");
  await page.getByRole("link", { name: "Fleet guide", exact: true }).click();
  await expect(page).toHaveURL(/\/memory\/guide$/);
  await page.reload();
  await expect(page.getByText("Guide contents", { exact: true })).toBeVisible();
  await expect(navigation(page).getByRole("link", { name: "Memory", exact: true })).toHaveAttribute("aria-current", "page");
  await navigation(page).getByRole("link", { name: "Settings", exact: true }).click();
  await page.goBack();
  await expect(page).toHaveURL(/\/memory\/guide$/);
  await expect(navigation(page).getByRole("link", { name: "Memory", exact: true })).toHaveAttribute("aria-current", "page");
  await page.getByRole("button", { name: "Collapse fleet sidebar" }).click();
  await expect(entry).toBeVisible();
  await entry.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings$/);
});

test("Fleet management mobile drawer closes and section links fit narrow screens", async ({ page }) => {
  await fixture(page);
  await page.setViewportSize({ width: 320, height: 740 });
  await page.goto("/memory/guide");
  const opener = page.getByRole("button", { name: "Open fleet agents" });
  await opener.click();
  const drawer = page.getByRole("dialog", { name: "Fleet agents" });
  const entry = drawer.getByRole("link", { name: "Fleet management", exact: true });
  await expect(entry).toBeFocused();
  await entry.click();
  await expect(drawer).not.toBeVisible();
  await expect(opener).toBeFocused();
  await expect(page).toHaveURL(/\/settings$/);
  const memory = navigation(page).getByRole("link", { name: "Memory", exact: true });
  await expect(memory).toBeInViewport();
  await memory.click();
  await expect(page.getByRole("heading", { name: "Memory", exact: true })).toBeVisible();
  await expect(memory).toHaveAttribute("aria-current", "page");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("Fleet management can reach Memory when the Agent roster is unavailable", async ({ page }) => {
  await fixture(page, { rosterUnavailable: true });
  await page.goto("/settings/");
  await expect(page.getByRole("heading", { name: "Fleet roster unavailable" })).toBeVisible();
  await navigation(page).getByRole("link", { name: "Memory", exact: true }).click();
  await expect(page.getByRole("link", { name: "Fleet guide", exact: true })).toBeVisible();
  await expect(sidebar(page).getByRole("link", { name: "Fleet management", exact: true })).toHaveAttribute("aria-current", "true");
});
