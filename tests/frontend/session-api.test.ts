import assert from "node:assert/strict";
import test from "node:test";
import { getSession } from "../../frontend/src/api.ts";

test("getSession encodes optional transcript pagination params", async () => {
  const originalFetch = globalThis.fetch;
  const calls: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return new Response(
      JSON.stringify({
        id: "session one",
        dir: "/tmp/session",
        kind: "primary",
        active: true,
        started_at: "2026-05-07T10:10:10Z",
        last_active_at: "2026-05-07T10:10:10Z",
        turns: 1,
        preview: "hello",
        token_usage: { input_tokens: 0, output_tokens: 0 },
        messages: [],
      }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" },
      },
    );
  }) as typeof fetch;

  try {
    await getSession("session one", { before: "msg/1", limit: 25 });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    "/api/sessions/session%20one?before=msg%2F1&limit=25",
  ]);
});
