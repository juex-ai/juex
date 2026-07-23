import assert from "node:assert/strict";
import test from "node:test";
import {
  APIError,
  getAgentConfig,
  getSession,
  listSessions,
  startTurn,
  subscribeEvents,
  uploadSessionAttachment,
} from "../../frontend/src/api.ts";

test("fleet API errors expose nested validation messages", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () =>
    new Response(
      JSON.stringify({
        error: {
          code: "invalid_config",
          message: "invalid workspace config: expected model",
        },
      }),
      {
        status: 400,
        statusText: "Bad Request",
        headers: { "Content-Type": "application/json" },
      },
    )) as typeof fetch;

  try {
    await assert.rejects(
      () => getAgentConfig("alpha"),
      (error: unknown) =>
        error instanceof APIError &&
        error.message === "invalid workspace config: expected model",
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("agent API calls use the selected fleet route prefix", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  const calls: string[] = [];
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { location: { pathname: "/agents/agent%20one/history" } },
  });
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return new Response(JSON.stringify({ sessions: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  try {
    await listSessions();
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow) {
      Object.defineProperty(globalThis, "window", originalWindow);
    } else {
      delete (globalThis as { window?: unknown }).window;
    }
  }

  assert.deepEqual(calls, ["/agents/agent%20one/api/sessions"]);
});

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

test("subscribeEvents forwards open and browser event callbacks", () => {
  const originalEventSource = globalThis.EventSource;
  let source: FakeEventSource | undefined;
  let opened = 0;
  let eventID = "";
  class FakeEventSource {
    readonly listeners = new Map<string, Array<(event: Event) => void>>();
    readonly url: string;
    closed = false;

    constructor(url: string) {
      this.url = url;
      source = this;
    }

    addEventListener(type: string, listener: (event: Event) => void) {
      const listeners = this.listeners.get(type) ?? [];
      listeners.push(listener);
      this.listeners.set(type, listeners);
    }

    emit(type: string, event: Event) {
      for (const listener of this.listeners.get(type) ?? []) listener(event);
    }

    close() {
      this.closed = true;
    }
  }
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;

  try {
    const unsubscribe = subscribeEvents("session one", {
      since: "cursor/1",
      onOpen: () => {
        opened += 1;
      },
      onEvent: (event) => {
        eventID = event.id;
      },
    });
    assert.equal(
      source?.url,
      "/api/sessions/session%20one/events?since=cursor%2F1",
    );
    source?.emit("open", new Event("open"));
    source?.emit(
      "message",
      new MessageEvent("message", {
        data: JSON.stringify({
          id: "evt-1",
          type: "turn.started",
          ts: "2026-07-23T00:00:00Z",
          payload: { input: "hello" },
          status: {
            session: {
              id: "session one",
              state: "turn_active",
              working: true,
              pending_count: 0,
              max_pending_inputs: 4,
              can_accept_input: true,
            },
            tools: [],
            token_usage: { input_tokens: 0, output_tokens: 0 },
          },
        }),
      }),
    );
    unsubscribe();

    assert.equal(opened, 1);
    assert.equal(eventID, "evt-1");
    assert.equal(source?.closed, true);

    const emptyCursorUnsubscribe = subscribeEvents("empty cursor", {
      since: "",
      onEvent: () => {},
    });
    assert.equal(
      source?.url,
      "/api/sessions/empty%20cursor/events?since=",
    );
    emptyCursorUnsubscribe();
  } finally {
    globalThis.EventSource = originalEventSource;
  }
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
