import { createRequire } from "node:module";

const require = createRequire(
  new URL("../../../frontend/package.json", import.meta.url),
);
const { expect, test } = require("@playwright/test");

const usage = {
  total: {
    input_tokens: 1_500,
    cached_input_tokens: 600,
    output_tokens: 300,
  },
  by_model: {
    "openai:gpt-5": {
      input_tokens: 1_000,
      cached_input_tokens: 400,
      output_tokens: 250,
    },
    "anthropic:claude": {
      input_tokens: 500,
      cached_input_tokens: 200,
      output_tokens: 50,
    },
  },
};

async function openThreadExplorer(page, tokenUsage = usage, extraThreads = {}) {
  await page.route("**/api/fleet/events", (route) => route.abort());
  await page.route("**/api/resource-events", (route) => route.abort());
  await page.route("**/api/agents", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify([
        {
          id: "test-agent",
          name: "Test Agent",
          workspace: "/tmp/juex-browser-test",
          enabled: true,
          autostart: false,
          binding: "bound",
          runtime_health: "healthy",
          runtime_present: true,
          process_alive: true,
          endpoint_reachable: true,
          endpoint_matched: true,
        },
      ]),
    }),
  );
  await page.route("**/agents/test-agent/api/threads", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        active_threads: [
          {
            thread_id: "usage-thread",
            alias: "Usage Thread",
            retention_state: "active",
            execution_state: "idle",
            created_at: "2026-09-03T00:00:00Z",
            last_activity_at: "2026-09-03T00:00:00Z",
            pending_input_count: 0,
            turn_count: 2,
            generation_count: 1,
            current_generation_id: "g1",
            current_context_tokens: 123,
            token_usage: tokenUsage,
            thread_revision: 1,
          },
        ],
        archived_threads: [],
        ...extraThreads,
      }),
    }),
  );

  await page.goto("/agents/test-agent/threads");
  await expect(page.getByText("Usage Thread")).toBeVisible();
}

test("Thread Explorer loads usage from the Agent index and reveals exact per-model values by keyboard", async ({
  page,
}) => {
  await openThreadExplorer(page);

  const trigger = page.locator('[data-thread-id="usage-thread"]').getByRole("button", {
    name: "1.8k tokens. Show token usage details",
  });
  await trigger.focus();
  await page.keyboard.press("Enter");

  const details = page.getByRole("dialog", { name: "Token usage details" });
  await expect(details).toBeVisible();
  await expect(details).toBeFocused();
  await expect(details).toContainText("1,800 total tokens");
  await expect(details).toContainText("openai:gpt-5");
  await expect(details).toContainText("anthropic:claude");
  await expect(details).toContainText("1,500");
  await expect(details).toContainText("600");
  await expect(details).toContainText("300");
});

test("touch users can open a persistent usage disclosure", async ({ browser }) => {
  const context = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 844 },
  });
  const page = await context.newPage();
  await openThreadExplorer(page);

  await page
    .locator('[data-thread-id="usage-thread"]')
    .getByRole("button", { name: "1.8k tokens. Show token usage details" })
    .tap();
  await expect(
    page.getByRole("dialog", { name: "Token usage details" }),
  ).toContainText("openai:gpt-5");
  await context.close();
});

test("long model breakdowns stay viewport-bounded and keyboard-scrollable", async ({
  page,
}) => {
  const byModel = Object.fromEntries(
    Array.from({ length: 40 }, (_, index) => [
      `provider:model-${String(index).padStart(2, "0")}`,
      { input_tokens: 40 - index, output_tokens: index + 1 },
    ]),
  );
  await openThreadExplorer(page, { ...usage, by_model: byModel });

  const trigger = page.locator('[data-thread-id="usage-thread"]').getByRole("button", {
    name: "1.8k tokens. Show token usage details",
  });
  await trigger.focus();
  await page.keyboard.press("Enter");

  const details = page.getByRole("dialog", { name: "Token usage details" });
  await expect(details).toBeFocused();
  const dimensions = await details.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
    viewportHeight: window.innerHeight,
    overflowY: getComputedStyle(element).overflowY,
  }));
  expect(dimensions.overflowY).toBe("auto");
  expect(dimensions.clientHeight).toBeLessThanOrEqual(
    dimensions.viewportHeight - 32,
  );
  expect(dimensions.scrollHeight).toBeGreaterThan(dimensions.clientHeight);

  await page.keyboard.press("End");
  await expect
    .poll(() => details.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
});

for (const width of [1280, 390]) {
  test(`Threads total includes Main and archived usage, merges models and refreshes at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    const makeThread = (id, retention) => ({
      thread_id: id, alias: id === "0" ? "Usage Thread" : id,
      retention_state: retention, execution_state: "idle", created_at: "2026-09-03T00:00:00Z",
      last_activity_at: "2026-09-03T00:00:00Z", pending_input_count: 0, turn_count: 2,
      generation_count: 1, current_generation_id: "g1", current_context_tokens: 123,
      token_usage: usage, thread_revision: 1,
    });
    const threads = { active_threads: [makeThread("0", "active"), makeThread("worker", "active")], archived_threads: [makeThread("old", "archived")] };
    await openThreadExplorer(page, usage, threads);
    const total = page.getByRole("group", { name: "Total token usage" });
    await expect(total).toContainText("5.4k tokens");
    await total.getByRole("button").click();
    const details = page.getByRole("dialog", { name: "Token usage details" });
    await expect(details).toContainText("5,400 total tokens");
    await expect(details).toContainText("4,500");
    await expect(details).toContainText("1,800");
    await expect(details.getByText("openai:gpt-5", { exact: true })).toHaveCount(1);
    await expect(details.getByText("openai:gpt-5", { exact: true }).locator("..")).toContainText("3,000");
    expect((await details.boundingBox()).width).toBeLessThanOrEqual(width - 32);
    await page.keyboard.press("Escape");
    await page.route("**/api/threads/worker/archive", async (route) => {
      threads.archived_threads.push({ ...threads.active_threads.pop(), retention_state: "archived" });
      await route.fulfill({ contentType: "application/json", body: "{}" });
    });
    await page.locator('[data-thread-id="worker"]').getByRole("button", { name: "Archive thread", exact: true }).click();
    await expect(page.locator('[data-thread-id="worker"]').getByRole("button", { name: "Unarchive thread", exact: true })).toBeVisible();
    await expect(total).toContainText("5.4k tokens");
    await page.route("**/api/threads/old", async (route) => {
      expect(route.request().method()).toBe("DELETE");
      threads.archived_threads.splice(threads.archived_threads.findIndex((thread) => thread.thread_id === "old"), 1);
      await route.fulfill({ contentType: "application/json", body: "{}" });
    });
    page.once("dialog", (dialog) => dialog.accept());
    await page.locator('[data-thread-id="old"]').getByRole("button", { name: "Delete thread permanently" }).click();
    await expect(total).toContainText("3.6k tokens");
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
  });
}
