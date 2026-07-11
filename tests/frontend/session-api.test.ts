import assert from "node:assert/strict";
import test from "node:test";
import { getSession, startTurn, uploadSessionAttachment } from "../../frontend/src/api.ts";

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

test("startTurn includes uploaded attachments", async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ input: string; init?: RequestInit }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input: String(input), init });
    return new Response(JSON.stringify({ turn_id: "turn-1" }), {
      status: 202,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  try {
    await startTurn("session one", "", [
      {
        artifact_path: ".juex/artifacts/media/session one/image.png",
        media_type: "image/png",
        sha256: "abc",
      },
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, "/api/sessions/session%20one/turns");
  assert.equal(calls[0].init?.method, "POST");
  assert.deepEqual(JSON.parse(String(calls[0].init?.body)), {
    prompt: "",
    attachments: [
      {
        artifact_path: ".juex/artifacts/media/session one/image.png",
        media_type: "image/png",
        sha256: "abc",
      },
    ],
  });
});

test("uploadSessionAttachment posts multipart file data", async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ input: string; init?: RequestInit }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input: String(input), init });
    return new Response(
      JSON.stringify({
        artifact_path: ".juex/artifacts/media/session/image.png",
        media_type: "image/png",
      }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" },
      },
    );
  }) as typeof fetch;

  try {
    await uploadSessionAttachment(
      "session/one",
      new File(["png"], "screen.png", { type: "image/png" }),
    );
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, "/api/sessions/session%2Fone/attachments");
  assert.equal(calls[0].init?.method, "POST");
  assert.ok(calls[0].init?.body instanceof FormData);
});
