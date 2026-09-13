import assert from "node:assert/strict";
import test from "node:test";
import { createObservable } from "../../frontend/src/api.ts";

test("createObservable posts a managed command definition unchanged", async () => {
  const originalFetch = globalThis.fetch;
  let body: unknown;
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    body = JSON.parse(String(init?.body));
    return new Response(JSON.stringify({ id: "events", state: "running" }), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
  const request = {
    id: "events",
    type: "command" as const,
    command_config: {
      command: "event-cli",
      streams: ["stdout"],
      batch: { interval_seconds: 10, max_chars: 1000 },
    },
  };
  try {
    await createObservable(request);
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.deepEqual(body, request);
});
