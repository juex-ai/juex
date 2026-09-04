import { createRequire } from "node:module";

const require = createRequire(
  new URL("../../../frontend/package.json", import.meta.url),
);
const { expect, test } = require("@playwright/test");

const imageDigest =
  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const fileDigest =
  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const imagePath = `event-media/${imageDigest}.png`;
const filePath = `event-media/${fileDigest}.txt`;
const observationContent = [
  "MCP notification",
  "server: wechat-wire",
  "event_type: notification",
  "content:",
  JSON.stringify({ content: "Alice sent a new photo" }),
].join("\n");
const observationText = [
  "Observable observation",
  "observation_id: obs-browser",
  "observable_id: mcp:wechat-wire",
  "kind: notification",
  "severity: info",
  "window_start: 1",
  "window_end: 2",
  `content_bytes: ${Buffer.byteLength(observationContent)}`,
  "content:",
  observationContent,
  "attachments:",
  `- image source=/tmp/photo.png artifact=${imagePath} (image/png, 68 bytes, sha256=${imageDigest}, 1x1)`,
  `- file source=/tmp/details.txt artifact=${filePath} (text/plain, 18 bytes, sha256=${fileDigest})`,
].join("\n");

async function openObservationThread(page, { filePreviewError = false } = {}) {
  await page.route("**/api/fleet/events", (route) => route.abort());
  await page.route("**/api/resource-events", (route) => route.abort());
  await page.route(
    "**/agents/test-agent/api/threads/observation-thread/events**",
    (route) => route.abort(),
  );
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
  await page.route(
    "**/agents/test-agent/api/threads/observation-thread/context",
    (route) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ messages: [], estimated_tokens: 0 }),
      }),
  );
  await page.route(
    "**/agents/test-agent/api/threads/observation-thread/status",
    (route) => route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        cursor: "cursor-1",
        thread: {
          id: "observation-thread",
          alias: "Observation Thread",
          state: "idle",
          working: false,
          pending_count: 0,
          max_pending_inputs: 8,
          can_accept_input: true,
        },
        tools: [],
        token_usage: { input_tokens: 0, output_tokens: 0 },
      }),
    }),
  );
  await page.route("**/agents/test-agent/api/threads/observation-thread", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        thread_id: "observation-thread",
        alias: "Observation Thread",
        dir: "/tmp/juex-browser-test/.juex/threads/observation-thread",
        retention_state: "active",
        execution_state: "idle",
        created_at: "2026-09-04T00:00:00Z",
        last_activity_at: "2026-09-04T00:00:00Z",
        revision: 1,
        generation_id: "g000001",
        turn_count: 1,
        pending_input_count: 0,
        token_usage: {
          total: { input_tokens: 0, output_tokens: 0 },
          by_model: {},
        },
        items: [
          {
            type: "message",
            seq: 1,
            at: "2026-09-04T00:00:00Z",
            message: {
              id: "msg-observation",
              role: "user",
              kind: "observation",
              blocks: [
                { type: "text", text: observationText },
                {
                  type: "image",
                  media: {
                    artifact_path: imagePath,
                    media_type: "image/png",
                    original_bytes: 68,
                    width: 1,
                    height: 1,
                  },
                },
              ],
            },
          },
        ],
        event_cursor: "cursor-1",
        has_more_before: false,
      }),
    }),
  );
  await page.route("**/agents/test-agent/api/files/content?root=artifact**", (route) => {
    if (filePreviewError) {
      return route.fulfill({
        status: 415,
        contentType: "application/json",
        body: JSON.stringify({
          error: "unsupported_media_type",
          message: "binary file preview is not supported",
          retryable: false,
        }),
      });
    }
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        path: filePath,
        content: "attachment details",
        kind: "text",
        size: 18,
        truncated: false,
      }),
    });
  });
  await page.route("**/agents/test-agent/api/media?root=artifact**", (route) =>
    route.fulfill({
      contentType: "image/png",
      body: Buffer.from(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=",
        "base64",
      ),
    }),
  );

  await page.goto("/agents/test-agent/threads/observation-thread");
}

test("Observation rows expose source, content, image lightbox, and file preview", async ({
  page,
}) => {
  await openObservationThread(page);

  const observation = page.locator('[data-external-event-kind="observation"]');
  await expect(observation.locator("[data-external-event-label]")).toHaveText(
    "observation:mcp:wechat-wire",
  );
  await expect(observation.locator("[data-external-event-preview]")).toHaveText(
    "Alice sent a new photo",
  );

  await observation.locator("[data-external-event-toggle]").click();
  await expect(observation.locator("[data-observation-title]")).toHaveText(
    "Observation",
  );
  await expect(
    observation.locator("[data-observation-attachments] img"),
  ).toBeVisible();

  await observation.locator("[data-observation-attachments] img").click();
  await expect(
    page.getByRole("dialog", { name: `Preview ${imageDigest}.png` }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Close image preview" }).click();

  await observation.getByRole("button", { name: "Preview details.txt" }).click();
  await expect(page.getByRole("dialog", { name: "details.txt" })).toContainText(
    "attachment details",
  );
});

test("one failed file preview leaves the Observation readable", async ({ page }) => {
  await openObservationThread(page, { filePreviewError: true });

  const observation = page.locator('[data-external-event-kind="observation"]');
  await observation.locator("[data-external-event-toggle]").click();
  await observation.getByRole("button", { name: "Preview details.txt" }).click();

  await expect(page.getByRole("alert")).toHaveText(
    "binary file preview is not supported",
  );
  await expect(observation.locator("[data-observation-title]")).toHaveText(
    "Observation",
  );
  await expect(observation.locator("[data-external-event-body]")).toContainText(
    "Alice sent a new photo",
  );
});
