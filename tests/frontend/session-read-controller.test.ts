import { strict as assert } from "node:assert";
import test from "node:test";

import {
  createSessionReadController,
  isLatestSessionRoute,
  type SessionReadControllerPorts,
} from "../../frontend/src/lib/session-read-controller.ts";
import {
  createSessionReadState,
  type SessionReadState,
} from "../../frontend/src/lib/session-read-state.ts";
import type {
  ActiveContextSnapshot,
  AgentRuntimeStatusSnapshot,
  BrowserEvent,
  MediaRef,
  Message,
  SessionShowResponse,
  StartTurnResponse,
} from "../../frontend/src/types.ts";

test("isLatestSessionRoute compares route identity", () => {
  assert.equal(isLatestSessionRoute({ id: "s1" }, "s1"), true);
  assert.equal(isLatestSessionRoute({ id: "s1" }, "s2"), false);
});

test("refresh ignores stale session results after route changes", async () => {
  const states: SessionShowResponse[] = [];
  let resolveSession: (value: SessionShowResponse) => void = () => {};
  let contextCalls = 0;
  const controller = createSessionReadController({
    ...ports(),
    onStateChange: (state) => {
      if (state.data) states.push(state.data);
    },
    getSession: (id) =>
      new Promise<SessionShowResponse>((resolve) => {
        resolveSession = () => resolve(session(id));
      }),
    getSessionContext: async () => {
      contextCalls++;
      return activeContext();
    },
  });

  controller.setRoute("old");
  const refresh = controller.refresh("old", { recordLoadFailure: true });
  controller.setRoute("new");
  resolveSession(session("old"));
  await refresh;

  assert.deepEqual(states, []);
  assert.equal(contextCalls, 0);
});

test("live events are ignored after route changes or subscription cleanup", () => {
  let latestState = createSessionReadState();
  let liveEvent: (event: BrowserEvent) => void = () => {};
  const controller = createSessionReadController({
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
  const controller = createSessionReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      subscribedSince = opts.since;
      onEvent = opts.onEvent;
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.subscribeLiveEvents("s1", {
    since: "snapshot-1",
    onStatus: (status) => {
      projectedStatus = status;
    },
  });
  onEvent(turnStartedEvent("current"));

  assert.equal(subscribedSince, "snapshot-1");
  assert.equal(projectedStatus?.cursor, "event-current");
  assert.equal(projectedStatus?.session.working, true);
});

test("stream reopen refreshes status when no newer event arrives", async () => {
  let onOpen: () => void = () => {};
  let projectedStatus: AgentRuntimeStatusSnapshot | undefined;
  const controller = createSessionReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      onOpen = opts.onOpen ?? (() => {});
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.subscribeLiveEvents("s1", {
    loadStatus: async () => runtimeStatus("reconnected"),
    onStatus: (status) => {
      projectedStatus = status;
    },
  });
  onOpen();
  await Promise.resolve();

  assert.equal(projectedStatus?.cursor, "reconnected");
});

test("stream event wins over an older reopen status request", async () => {
  let onOpen: () => void = () => {};
  let onEvent: (event: BrowserEvent) => void = () => {};
  let resolveStatus: (status: AgentRuntimeStatusSnapshot) => void = () => {};
  const projectedCursors: Array<string | undefined> = [];
  const controller = createSessionReadController({
    ...ports(),
    subscribeEvents: (_id, opts) => {
      onOpen = opts.onOpen ?? (() => {});
      onEvent = opts.onEvent;
      return () => {};
    },
  });

  controller.setRoute("s1");
  controller.subscribeLiveEvents("s1", {
    loadStatus: () =>
      new Promise((resolve) => {
        resolveStatus = resolve;
      }),
    onStatus: (status) => {
      projectedCursors.push(status.cursor);
    },
  });
  onOpen();
  onEvent(turnStartedEvent("newer"));
  resolveStatus(runtimeStatus("older"));
  await Promise.resolve();

  assert.deepEqual(projectedCursors, ["event-newer"]);
});

test("submitPrompt ignores late startTurn results after route changes", async () => {
  let latestState = createSessionReadState();
  let resolveStart: (value: StartTurnResponse) => void = () => {};
  const controller = createSessionReadController({
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
  let latestState = createSessionReadState();
  let rejectStart: (reason: unknown) => void = () => {};
  let loggedErrors = 0;
  const controller = createSessionReadController({
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

test("submitPrompt forwards attachments and projects optimistic image blocks", async () => {
  let latestState = createSessionReadState();
  let submittedAttachments: MediaRef[] | undefined;
  const attachments: MediaRef[] = [
    {
      artifact_path: ".juex/artifacts/media/image.png",
      media_type: "image/png",
      sha256: "abc123",
      original_bytes: 12,
    },
  ];
  const controller = createSessionReadController({
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

test("controller interprets navigation, refresh, and timer effects", async () => {
  const timers = new FakeTimers();
  let refreshed = 0;
  let routeCleared = 0;
  let sessionsChanged = 0;
  const navigations: Array<{ sessionID: string; state: unknown }> = [];
  let latestState = createSessionReadState();
  const newCommand = {
    name: "/new",
    text: "Created",
    status: { session_id: "s2" },
  };
  const controller = createSessionReadController({
    ...ports(),
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    onStateChange: (state) => {
      latestState = state;
    },
    getSession: async (id) => {
      refreshed++;
      return session(id);
    },
    getSessionContext: async () => activeContext(),
    startTurn: async (): Promise<StartTurnResponse> => ({
      command: newCommand,
    }),
  });
  controller.configureNavigation({
    clearRouteState: () => {
      routeCleared++;
    },
    dispatchSessionsChanged: () => {
      sessionsChanged++;
    },
    navigateToSession: (sessionID, state) => {
      navigations.push({ sessionID, state });
    },
  });
  controller.setRoute("s1");

  await controller.submitPrompt("s1", "/new");
  controller.showComposerHint("Enter a message");
  controller.runSessionReadResult({
    state: controller.currentState(),
    effects: [
      { type: "refresh", preserveLiveMessages: true },
      { type: "clearRouteState" },
    ],
  });
  await flushPromises();

  assert.equal(sessionsChanged, 1);
  assert.deepEqual(navigations, [
    {
      sessionID: "s2",
      state: {
        commandInput: "/new",
        command: newCommand,
      },
    },
  ]);
  assert.equal(routeCleared, 1);
  assert.equal(refreshed, 1);
  assert.equal(latestState?.composerHint, "Enter a message");

  timers.runNext();
  assert.equal(latestState?.composerHint, null);
});

function ports(): SessionReadControllerPorts & { initialState: SessionReadState } {
  const initialState = createSessionReadState();
  return {
    initialState,
    onStateChange: () => {},
    getSession: async (id) => session(id),
    getSessionContext: async () => activeContext(),
    startTurn: async (): Promise<StartTurnResponse> => ({ turn_id: "turn-1" }),
    subscribeEvents: (_id, _opts) => () => {},
  };
}

function session(id: string, messages: Message[] = []): SessionShowResponse {
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
    session: {
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
