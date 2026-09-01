import { strict as assert } from "node:assert";
import test from "node:test";

import {
  createThreadReadController,
  isLatestThreadRoute,
  type ThreadReadControllerPorts,
} from "../../frontend/src/lib/thread-read-controller.ts";
import {
  createThreadReadState,
  type ThreadReadState,
} from "../../frontend/src/lib/thread-read-state.ts";
import type {
  ActiveContextSnapshot,
  AgentRuntimeStatusSnapshot,
  BrowserEvent,
  MediaRef,
  Message,
  ThreadShowResponse,
  StartTurnResponse,
} from "../../frontend/src/types.ts";

test("isLatestThreadRoute compares route identity", () => {
  assert.equal(isLatestThreadRoute({ id: "s1" }, "s1"), true);
  assert.equal(isLatestThreadRoute({ id: "s1" }, "s2"), false);
});

test("refresh ignores stale thread results after route changes", async () => {
  const states: ThreadShowResponse[] = [];
  let resolveThread: (value: ThreadShowResponse) => void = () => {};
  let contextCalls = 0;
  const controller = createThreadReadController({
    ...ports(),
    onStateChange: (state) => {
      if (state.data) states.push(state.data);
    },
    getThread: (id) =>
      new Promise<ThreadShowResponse>((resolve) => {
        resolveThread = () => resolve(thread(id));
      }),
    getThreadContext: async () => {
      contextCalls++;
      return activeContext();
    },
  });

  controller.setRoute("old");
  const refresh = controller.refresh("old", { recordLoadFailure: true });
  controller.setRoute("new");
  resolveThread(thread("old"));
  await refresh;

  assert.deepEqual(states, []);
  assert.equal(contextCalls, 0);
});

test("live events are ignored after route changes or subscription cleanup", () => {
  let latestState = createThreadReadState();
  let liveEvent: (event: BrowserEvent) => void = () => {};
  const controller = createThreadReadController({
    ...ports(),
    onStateChange: (state) => {
      latestState = state;
    },
    subscribeEvents: (_id, opts) => {
      liveEvent = opts.onEvent;
      return () => {};
    },
  });

  controller.setRoute("s1");
  const cleanup = controller.subscribeLiveEvents("s1");
  controller.setRoute("s2");
  liveEvent(turnStartedEvent("stale-route"));
  assert.equal(latestState.projection.messages.length, 0);

  controller.setRoute("s1");
  cleanup();
  liveEvent(turnStartedEvent("after-cleanup"));
  assert.equal(latestState.projection.messages.length, 0);
});

test("live events replace canonical status from the requested cursor", () => {
  let onEvent: (event: BrowserEvent) => void = () => {};
  let subscribedSince: string | undefined;
  let projectedStatus: AgentRuntimeStatusSnapshot | undefined;
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      subscribedSince = opts.since;
      onEvent = opts.onEvent;
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.configureLiveStatus({
    load: async () => runtimeStatus("snapshot"),
    apply: (_threadID, status) => {
      projectedStatus = status;
    },
    clear: () => {},
  });
  controller.subscribeLiveEvents("s1", { since: "snapshot-1" });
  onEvent(turnStartedEvent("current"));

  assert.equal(subscribedSince, "snapshot-1");
  assert.equal(projectedStatus?.cursor, "event-current");
  assert.equal(projectedStatus?.thread.working, true);
});

test("recreated live streams resume after the last applied event", () => {
  const subscribedSince: Array<string | undefined> = [];
  const eventHandlers: Array<(event: BrowserEvent) => void> = [];
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      subscribedSince.push(opts.since);
      eventHandlers.push(opts.onEvent);
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.resetForRoute();
  const cleanup = controller.subscribeLiveEvents("s1", {
    since: "bootstrap-s1",
  });
  eventHandlers[0](turnStartedEvent("first"));
  eventHandlers[0](turnStartedEvent("second"));
  cleanup();

  controller.subscribeLiveEvents("s1", { since: "bootstrap-s1" });

  controller.setRoute("s2");
  controller.resetForRoute();
  controller.subscribeLiveEvents("s2", { since: "bootstrap-s2" });

  assert.deepEqual(subscribedSince, [
    "bootstrap-s1",
    "event-second",
    "bootstrap-s2",
  ]);
});

test("resume cursor follows applied status and ignores empty transient cursors", () => {
  const subscribedSince: Array<string | undefined> = [];
  const eventHandlers: Array<(event: BrowserEvent) => void> = [];
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      subscribedSince.push(opts.since);
      eventHandlers.push(opts.onEvent);
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.resetForRoute();
  const cleanup = controller.subscribeLiveEvents("s1", {
    since: "bootstrap-s1",
  });
  eventHandlers[0]({
    ...turnStartedEvent("durable"),
    id: "different-browser-event-id",
    status: runtimeStatus("status-durable"),
  });
  eventHandlers[0]({
    ...turnStartedEvent("transient"),
    id: "transient-browser-event-id",
    status: runtimeStatus(""),
  });
  cleanup();

  controller.subscribeLiveEvents("s1", { since: "bootstrap-s1" });

  assert.deepEqual(subscribedSince, ["bootstrap-s1", "status-durable"]);
});

test("status calibration cannot advance the transcript resume cursor", async () => {
  const subscribedSince: Array<string | undefined> = [];
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      subscribedSince.push(opts.since);
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.resetForRoute();
  controller.configureLiveStatus({
    load: async () => runtimeStatus("calibration-ahead"),
    apply: () => {},
    clear: () => {},
  });
  const cleanup = controller.subscribeLiveEvents("s1", {
    since: "bootstrap-s1",
  });
  await Promise.resolve();
  cleanup();

  controller.subscribeLiveEvents("s1", { since: "bootstrap-s1" });

  assert.deepEqual(subscribedSince, ["bootstrap-s1", "bootstrap-s1"]);
});

test("subscription and stream reopen refresh status when no event arrives", async () => {
  let onOpen: () => void = () => {};
  let projectedStatus: AgentRuntimeStatusSnapshot | undefined;
  let refreshes = 0;
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      onOpen = opts.onOpen ?? (() => {});
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.configureLiveStatus({
    load: async () =>
      runtimeStatus(++refreshes === 1 ? "initial" : "reconnected"),
    apply: (_threadID, status) => {
      projectedStatus = status;
    },
    clear: () => {},
  });
  controller.subscribeLiveEvents("s1");
  await Promise.resolve();
  assert.equal(projectedStatus?.cursor, "initial");

  onOpen();
  await Promise.resolve();

  assert.equal(refreshes, 2);
  assert.equal(projectedStatus?.cursor, "reconnected");
});

test("status refresh failure leaves the live event stream usable", async () => {
  let onEvent: (event: BrowserEvent) => void = () => {};
  let refreshErrors = 0;
  let projectedStatus: AgentRuntimeStatusSnapshot | undefined;
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      onEvent = opts.onEvent;
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.configureLiveStatus({
    load: async () => {
      throw new Error("temporary status failure");
    },
    apply: (_threadID, status) => {
      projectedStatus = status;
    },
    clear: () => {},
    onRefreshError: () => {
      refreshErrors++;
    },
  });
  controller.subscribeLiveEvents("s1");
  await Promise.resolve();
  onEvent(turnStartedEvent("after-failure"));

  assert.equal(refreshErrors, 1);
  assert.equal(projectedStatus?.cursor, "event-after-failure");
});

test("stream event wins over an older initial status request", async () => {
  let onEvent: (event: BrowserEvent) => void = () => {};
  let resolveStatus: (status: AgentRuntimeStatusSnapshot) => void = () => {};
  const projectedCursors: Array<string | undefined> = [];
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      onEvent = opts.onEvent;
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.configureLiveStatus({
    load: () =>
      new Promise((resolve) => {
        resolveStatus = resolve;
      }),
    apply: (_threadID, status) => {
      projectedCursors.push(status.cursor);
    },
    clear: () => {},
  });
  controller.subscribeLiveEvents("s1");
  onEvent(turnStartedEvent("newer"));
  resolveStatus(runtimeStatus("older"));
  await Promise.resolve();

  assert.deepEqual(projectedCursors, ["event-newer"]);
});

test("subscription cleanup closes transport before clearing status", () => {
  const lifecycle: string[] = [];
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: () => () => {
      lifecycle.push("unsubscribe");
    },
  });

  controller.setRoute("s1");
  controller.configureLiveStatus({
    load: async () => runtimeStatus("initial"),
    apply: () => {},
    clear: (threadID) => {
      lifecycle.push(`clear:${threadID}`);
    },
  });

  controller.subscribeLiveEvents("s1")();

  assert.deepEqual(lifecycle, ["unsubscribe", "clear:s1"]);
});

test("configured live status receives only current stream failures", () => {
  let onError: ((event: Event) => void) | undefined;
  let streamErrors = 0;
  const controller = createThreadReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      onError = opts.onError;
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.configureLiveStatus({
    load: async () => runtimeStatus("initial"),
    apply: () => {},
    clear: () => {},
    onStreamError: () => {
      streamErrors++;
    },
  });
  const cleanup = controller.subscribeLiveEvents("s1");
  onError?.(new Event("error"));
  assert.equal(streamErrors, 1);

  controller.setRoute("s2");
  onError?.(new Event("error"));
  assert.equal(streamErrors, 1);

  controller.setRoute("s1");
  cleanup();
  onError?.(new Event("error"));
  assert.equal(streamErrors, 1);
});

test("submitPrompt ignores late startTurn results after route changes", async () => {
  let latestState = createThreadReadState();
  let resolveStart: (value: StartTurnResponse) => void = () => {};
  const controller = createThreadReadController({
    ...ports(),
    onStateChange: (state) => {
      latestState = state;
    },
    startTurn: async () =>
      new Promise<StartTurnResponse>((resolve) => {
        resolveStart = resolve;
      }),
  });

  controller.setRoute("s1");
  const submit = controller.submitPrompt("s1", "hello");
  controller.setRoute("s2");
  resolveStart({ turn_id: "turn-stale" });

  assert.equal(await submit, false);
  assert.equal(latestState.projection.messages.length, 0);
});

test("submitPrompt ignores late startTurn failures after route changes", async () => {
  let latestState = createThreadReadState();
  let rejectStart: (reason: unknown) => void = () => {};
  let loggedErrors = 0;
  const controller = createThreadReadController({
    ...ports(),
    onStateChange: (state) => {
      latestState = state;
    },
    logError: () => {
      loggedErrors++;
    },
    startTurn: async () =>
      new Promise<StartTurnResponse>((_resolve, reject) => {
        rejectStart = reject;
      }),
  });

  controller.setRoute("s1");
  const submit = controller.submitPrompt("s1", "hello");
  controller.setRoute("s2");
  rejectStart(new Error("late failure"));

  assert.equal(await submit, false);
  assert.equal(loggedErrors, 0);
  assert.equal(latestState.projection.messages.length, 0);
});

test("submitPrompt timestamps a pending compact before the request settles", async () => {
  let latestState = createThreadReadState();
  let resolveStart: (value: StartTurnResponse) => void = () => {};
  const controller = createThreadReadController({
    ...ports(),
    onStateChange: (state) => {
      latestState = state;
    },
    startTurn: async () =>
      new Promise<StartTurnResponse>((resolve) => {
        resolveStart = resolve;
      }),
  });

  controller.setRoute("s1");
  const submit = controller.submitPrompt("s1", "/compact");

  assert.match(
    latestState.projection.messages[0]?.created_at ?? "",
    /^\d{4}-\d{2}-\d{2}T/,
  );

  resolveStart({
    command: {
      name: "/compact",
      text: "Compacted",
      compact: { message_id: "compact-1" },
    },
  });
  assert.equal(await submit, true);
});

test("submitPrompt forwards attachments and projects optimistic image blocks", async () => {
  let latestState = createThreadReadState();
  let submittedAttachments: MediaRef[] | undefined;
  const attachments: MediaRef[] = [
    {
		artifact_path: "read-media/image.png",
      media_type: "image/png",
      sha256: "abc123",
      original_bytes: 12,
    },
  ];
  const controller = createThreadReadController({
    ...ports(),
    onStateChange: (state) => {
      latestState = state;
    },
    startTurn: async (
      _id,
      _prompt,
      nextAttachments,
    ): Promise<StartTurnResponse> => {
      submittedAttachments = nextAttachments;
      return { turn_id: "turn-1" };
    },
  });

  controller.setRoute("s1");
  const ok = await controller.submitPrompt("s1", "", attachments);

  assert.equal(ok, true);
  assert.deepEqual(submittedAttachments, attachments);
  assert.equal(
    latestState.projection.messages.some((message) =>
      message.blocks.some((block) => block.type === "image"),
    ),
    true,
  );
});

test("controller interprets refresh and timer effects", async () => {
  const timers = new FakeTimers();
  let refreshed = 0;
  let latestState = createThreadReadState();
  const newCommand = {
    name: "/new",
    text: "Created",
    status: { thread_id: "s2" },
  };
  const controller = createThreadReadController({
    ...ports(),
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    onStateChange: (state) => {
      latestState = state;
    },
    getThread: async (id) => {
      refreshed++;
      return thread(id);
    },
    getThreadContext: async () => activeContext(),
    startTurn: async (): Promise<StartTurnResponse> => ({
      command: newCommand,
    }),
  });
  controller.setRoute("s1");

  await controller.submitPrompt("s1", "/new");
  controller.showComposerHint("Enter a message");
  controller.runThreadReadResult({
    state: controller.currentState(),
    effects: [
      { type: "refresh", preserveLiveMessages: true },
    ],
  });
  await flushPromises();

  assert.equal(refreshed, 2);
  assert.equal(latestState?.composerHint, "Enter a message");

  timers.runNext();
  assert.equal(latestState?.composerHint, null);
});

function ports(): ThreadReadControllerPorts & { initialState: ThreadReadState } {
  const initialState = createThreadReadState();
  return {
    initialState,
    onStateChange: () => {},
    getThread: async (id) => thread(id),
    getThreadContext: async () => activeContext(),
    startTurn: async (): Promise<StartTurnResponse> => ({ turn_id: "turn-1" }),
    subscribeEvents: (_id, _opts) => () => {},
  };
}

function thread(id: string, messages: Message[] = []): ThreadShowResponse {
  return {
    id,
    dir: `/tmp/${id}`,
    kind: "primary",
    active: true,
    started_at: "2026-06-15T00:00:00Z",
    last_active_at: "2026-06-15T00:00:00Z",
    turns: 1,
    preview: "preview",
    token_usage: { input_tokens: 1, output_tokens: 1 },
    messages,
  };
}

function activeContext(): ActiveContextSnapshot {
  return { messages: [], estimated_tokens: 0 };
}

function turnStartedEvent(input: string): BrowserEvent {
  const cursor = `event-${input}`;
  return {
    id: cursor,
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: `turn-${input}`,
    payload: { input },
    status: runtimeStatus(cursor),
  };
}

function runtimeStatus(cursor: string): AgentRuntimeStatusSnapshot {
  return {
    cursor,
    thread: {
      id: "s1",
      state: "turn_active",
      working: true,
      pending_count: 0,
      max_pending_inputs: 4,
      can_accept_input: true,
    },
    turn: {
      id: "turn-1",
      state: "active",
      phase: "provider_iteration",
      streaming: true,
      can_interrupt: true,
      started_at: "",
      updated_at: "",
    },
    tools: [],
    token_usage: { input_tokens: 0, output_tokens: 0 },
  };
}

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

class FakeTimers {
  private nextID = 1;
  private timers: Array<{ id: number; callback: () => void; cleared: boolean }> = [];

  setTimeout = (callback: () => void, _ms: number) => {
    const id = this.nextID++;
    this.timers.push({ id, callback, cleared: false });
    return id as ReturnType<typeof setTimeout>;
  };

  clearTimeout = (handle: ReturnType<typeof setTimeout>) => {
    const id = Number(handle);
    const timer = this.timers.find((item) => item.id === id);
    if (timer) timer.cleared = true;
  };

  pendingCount() {
    return this.timers.filter((item) => !item.cleared).length;
  }

  runNext() {
    const timer = this.timers.find((item) => !item.cleared);
    if (!timer) return;
    timer.cleared = true;
    timer.callback();
  }
}
