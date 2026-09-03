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

async function openThreadExplorer(page, tokenUsage = usage) {
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

  const trigger = page.getByRole("button", {
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

  const trigger = page.getByRole("button", {
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
