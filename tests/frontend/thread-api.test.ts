import assert from "node:assert/strict";
import test from "node:test";
import {
  APIError,
  getAgentConfig,
  getThread,
  listThreads,
  startTurn,
  subscribeEvents,
  uploadThreadAttachment,
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
    value: { location: { pathname: "/agents/agent%20one/threads" } },
  });
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return new Response(JSON.stringify({ active_threads: [], archived_threads: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  try {
    await listThreads();
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow) {
      Object.defineProperty(globalThis, "window", originalWindow);
    } else {
      delete (globalThis as { window?: unknown }).window;
    }
  }

  assert.deepEqual(calls, ["/agents/agent%20one/api/threads"]);
});

test("listThreads preserves aggregate and per-model usage from the Agent index", async () => {
  const originalFetch = globalThis.fetch;
  const calls: string[] = [];
  const tokenUsage = {
    total: {
      input_tokens: 1_200,
      cached_input_tokens: 400,
      output_tokens: 300,
    },
    by_model: {
      "openai:gpt-5": {
        input_tokens: 1_200,
        cached_input_tokens: 400,
        output_tokens: 300,
      },
    },
  };
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return new Response(
      JSON.stringify({
        active_threads: [{ thread_id: "worker1", token_usage: tokenUsage }],
        archived_threads: [{ thread_id: "worker2", token_usage: tokenUsage }],
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }) as typeof fetch;

  try {
    const result = await listThreads();
    assert.deepEqual(result.active_threads[0].token_usage, tokenUsage);
    assert.deepEqual(result.archived_threads[0].token_usage, tokenUsage);
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, ["/api/threads"]);
});

test("getThread encodes optional transcript pagination params", async () => {
  const originalFetch = globalThis.fetch;
  const calls: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return new Response(
      JSON.stringify({
        thread_id: "thread one",
        alias: "worker_thread one",
        dir: "/tmp/thread",
        created_at: "2026-05-07T10:10:10.000Z",
        last_activity_at: "2026-05-07T10:10:10.000Z",
        retention_state: "active",
        execution_state: "idle",
        revision: 2,
        generation_id: "1",
        turn_count: 1,
        pending_input_count: 0,
        token_usage: { total: { input_tokens: 0, output_tokens: 0 }, by_model: {} },
        items: [],
      }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" },
      },
    );
  }) as typeof fetch;

  try {
    const result = await getThread("thread one", { before: "msg/1", limit: 25 });
    assert.equal(result.retention_state, "active");
    assert.equal(result.execution_state, "idle");
    assert.deepEqual(result.token_usage, { total: { input_tokens: 0, output_tokens: 0 }, by_model: {} });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    "/api/threads/thread%20one?before=msg%2F1&limit=25",
  ]);
});

test("getThread treats a null empty timeline as no messages", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () =>
    new Response(
      JSON.stringify({
        thread_id: "worker1",
        alias: "worker",
        dir: "/tmp/thread",
        created_at: "2026-09-01T00:00:00.000Z",
        last_activity_at: "2026-09-01T00:00:00.000Z",
        retention_state: "active",
        execution_state: "idle",
        revision: 1,
        generation_id: "g000001",
        turn_count: 0,
        pending_input_count: 0,
        token_usage: { total: { input_tokens: 0, output_tokens: 0 }, by_model: {} },
        items: null,
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    )) as typeof fetch;

  try {
    const result = await getThread("worker1");
    assert.deepEqual(result.messages, []);
  } finally {
    globalThis.fetch = originalFetch;
  }
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
    await startTurn("thread one", "", [
      {
		artifact_path: "threads/thread one/media/image.png",
        media_type: "image/png",
        sha256: "abc",
      },
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, "/api/threads/thread%20one/inputs");
  assert.equal(calls[0].init?.method, "POST");
  assert.deepEqual(JSON.parse(String(calls[0].init?.body)), {
    prompt: "",
    attachments: [
      {
		artifact_path: "threads/thread one/media/image.png",
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
    const unsubscribe = subscribeEvents("thread one", {
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
      "/api/threads/thread%20one/events?since=cursor%2F1",
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
            thread: {
              id: "thread one",
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

    // An empty cursor means the journal was empty when the transcript snapshot
    // was taken, so the browser still needs everything committed since. It asks
    // through a separate `replay` parameter, keeping `since` an opaque event ID.
    const emptyCursorUnsubscribe = subscribeEvents("empty cursor", {
      since: "",
      onEvent: () => {},
    });
    assert.equal(
      source?.url,
      "/api/threads/empty%20cursor/events?replay=journal-start",
    );
    emptyCursorUnsubscribe();

    const noCursorUnsubscribe = subscribeEvents("no cursor", {
      onEvent: () => {},
    });
    assert.equal(source?.url, "/api/threads/no%20cursor/events");
    noCursorUnsubscribe();
  } finally {
    globalThis.EventSource = originalEventSource;
  }
});

test("uploadThreadAttachment posts multipart file data", async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ input: string; init?: RequestInit }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input: String(input), init });
    return new Response(
      JSON.stringify({
		artifact_path: "threads/thread/media/image.png",
        media_type: "image/png",
      }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" },
      },
    );
  }) as typeof fetch;

  try {
    await uploadThreadAttachment(
      "thread/one",
      new File(["png"], "screen.png", { type: "image/png" }),
    );
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, "/api/threads/thread%2Fone/attachments");
  assert.equal(calls[0].init?.method, "POST");
  assert.ok(calls[0].init?.body instanceof FormData);
});
