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
  BrowserEvent,
  MediaRef,
  Message,
  SessionShowResponse,
  StartTurnResponse,
  TurnStatusResponse,
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

test("turn polling retries initial route-state failures and cancels timers", async () => {
  const timers = new FakeTimers();
  const turns: Array<TurnStatusResponse | Error> = [
    new Error("temporary"),
    { state: "running" },
    { state: "done" },
  ];
  let calls = 0;
  const controller = createSessionReadController({
    ...ports(),
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    getTurnStatus: async () => {
      const result = turns[calls++] ?? { state: "done" };
      if (result instanceof Error) throw result;
      return result;
    },
  });

  controller.setRoute("s1");
  const cleanup = controller.startTurnStatusPolling({
    sessionID: "s1",
    turnID: "turn-1",
    retryOnError: true,
  });
  await flushPromises();
  assert.equal(calls, 1);
  assert.equal(timers.pendingCount(), 1);

  timers.runNext();
  await flushPromises();
  assert.equal(calls, 2);
  assert.equal(timers.pendingCount(), 1);

  cleanup();
  timers.runNext();
  await flushPromises();
  assert.equal(calls, 2);
});

test("loaded turn polling does not retry transient failures", async () => {
  const timers = new FakeTimers();
  let calls = 0;
  const controller = createSessionReadController({
    ...ports(),
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    getTurnStatus: async () => {
      calls++;
      throw new Error("gone");
    },
  });

  controller.setRoute("s1");
  controller.startTurnStatusPolling({
    sessionID: "s1",
    turnID: "turn-1",
    retryOnError: false,
  });
  await flushPromises();

  assert.equal(calls, 1);
  assert.equal(timers.pendingCount(), 0);
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
  assert.equal(latestState.projection.status.kind, "idle");
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
    getTurnStatus: async () => ({ state: "done" }),
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
  return {
    id: `event-${input}`,
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: `turn-${input}`,
    payload: { input },
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
