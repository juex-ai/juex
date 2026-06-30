import { strict as assert } from "node:assert";
import test from "node:test";

import {
  clearComposerHint,
  createSessionReadState,
  markSessionProjectionIdle,
  projectComposerHint,
  projectInitialCommand,
  projectLiveBrowserEvent,
  projectLoadOlderFailed,
  projectLoadOlderStarted,
  projectLoadOlderSucceeded,
  projectPendingSubmit,
  projectPromptInputChanged,
  projectSessionLoadFailed,
  projectSessionLoaded,
  projectStartTurnFailed,
  projectStartTurnSucceeded,
  projectTurnStatus,
  resetSessionReadState,
} from "../../frontend/src/lib/session-read-state.ts";
import type {
  Message,
  SessionShowResponse,
} from "../../frontend/src/types.ts";

test("resetSessionReadState starts active turn reconciliation", () => {
  const state = resetSessionReadState(createSessionReadState(), {
    activeTurnID: "turn-1",
  });

  assert.equal(state.projection.turnActive, true);
  assert.deepEqual(state.projection.status, { kind: "running" });
  assert.equal(state.composerHint, null);
  assert.equal(state.loadingOlderMessages, false);
});

test("session load failure leaves loading and route reset clears stale data", () => {
  let state = projectSessionLoaded(
    createSessionReadState(),
    session("old", []),
  );
  state = projectSessionLoadFailed(
    state,
    new Error("session not found: missing"),
  );

  assert.equal(state.data, null);
  assert.equal(state.loadError, "session not found: missing");
  assert.equal(state.olderMessagesError, null);

  state = projectSessionLoaded(state, session("old", []));
  state = resetSessionReadState(state);
  assert.equal(state.data, null);
  assert.equal(state.loadError, null);
});

test("projectLiveBrowserEvent carries projection effects through controller", () => {
  const result = projectLiveBrowserEvent(createSessionReadState(), {
    id: "evt-1",
    type: "turn.completed",
    ts: "2026-06-15T00:00:00Z",
    payload: {
      duration_ms: 10,
      output_len: 5,
    },
  });

  assert.equal(result.state.projection.turnActive, false);
  assert.deepEqual(result.state.projection.status, { kind: "done" });
  assert.deepEqual(result.effects, [
    { type: "refresh" },
    { type: "scheduleIdleStatus" },
  ]);
});

test("projectTurnStatus reconciles running and done states", () => {
  let state = createSessionReadState();
  let result = projectTurnStatus(state, { state: "running", pending_count: 2 });
  state = result.state;
  assert.deepEqual(state.projection.status, { kind: "pending", count: 2 });
  assert.deepEqual(result.effects, []);

  result = projectTurnStatus(state, { state: "done" });
  assert.deepEqual(result.state.projection.status, { kind: "done" });
  assert.deepEqual(result.effects, [
    { type: "refresh" },
    { type: "scheduleIdleStatus" },
  ]);
});

test("projectStartTurnSucceeded records queued and optimistic turns", () => {
  let state = createSessionReadState();
  let result = projectStartTurnSucceeded(state, "second prompt", {
    queued: true,
    pending_count: 1,
  });
  state = result.state;
  assert.equal(state.projection.queuedInput.items.length, 1);
  assert.deepEqual(state.projection.status, { kind: "pending", count: 1 });

  result = projectStartTurnSucceeded(state, "new prompt", {
    turn_id: "turn-2",
  });
  assert.equal(result.state.projection.turnActive, true);
  assert.equal(result.state.projection.messages.at(-2)?.turn_id, "turn-2");
  assert.deepEqual(result.effects, []);
});

test("projectStartTurnSucceeded emits navigation effect for /new", () => {
  const result = projectStartTurnSucceeded(createSessionReadState(), "/new", {
    turn_id: "turn-new",
    command: {
      name: "/new",
      text: "Created new session",
      status: { session_id: "session-2" },
    },
  });

  assert.deepEqual(result.effects, [
    { type: "dispatchSessionsChanged" },
    {
      type: "navigateToSession",
      sessionID: "session-2",
      state: { activeTurnID: "turn-new" },
    },
  ]);
});

test("projectStartTurnSucceeded settles compact command with refresh effect", () => {
  let state = projectPendingSubmit(createSessionReadState(), "/compact");
  assert.equal(state.projection.compactActive, true);

  const result = projectStartTurnSucceeded(state, "/compact", {
    command: {
      name: "/compact",
      text: "Compacted",
      compact: { message_id: "compact-1" },
    },
  });

  assert.equal(result.state.projection.compactActive, false);
  assert.equal(
    result.state.projection.compactCommandInputs["compact-1"],
    "/compact",
  );
  assert.deepEqual(result.effects, [
    { type: "refresh", preserveLiveMessages: true },
    { type: "scheduleIdleStatus" },
  ]);
});

test("projectInitialCommand projects slash command and clears route state", () => {
  const result = projectInitialCommand(createSessionReadState(), "/status", {
    name: "/status",
    text: "ok",
  });

  assert.equal(result.state.projection.messages.length, 2);
  assert.deepEqual(result.effects, [
    { type: "clearRouteState" },
    { type: "scheduleIdleStatus" },
  ]);
});

test("projectInitialCommand preserves compact command input across refresh", () => {
  const result = projectInitialCommand(createSessionReadState(), "/compact", {
    name: "/compact",
    text: "Compacted",
    compact: { message_id: "compact-1" },
  });

  assert.equal(
    result.state.projection.compactCommandInputs["compact-1"],
    "/compact",
  );
  assert.deepEqual(result.effects, [
    { type: "refresh", preserveLiveMessages: true },
    { type: "clearRouteState" },
    { type: "scheduleIdleStatus" },
  ]);
});

test("load older state merges pages and records errors", () => {
  const base = projectSessionLoaded(
    createSessionReadState(),
    session("s1", [{ role: "user", blocks: [{ type: "text", text: "new" }] }]),
    { preserveLiveMessages: true },
  );

  let state = projectLoadOlderStarted(base);
  assert.equal(state.loadingOlderMessages, true);

  state = projectLoadOlderSucceeded(
    state,
    session("s1", [{ role: "user", blocks: [{ type: "text", text: "old" }] }]),
  );
  assert.equal(state.loadingOlderMessages, false);
  assert.deepEqual(
    state.data?.messages.map((message) =>
      message.blocks?.[0]?.type === "text" ? message.blocks[0].text : "",
    ),
    ["old", "new"],
  );

  state = projectLoadOlderFailed(projectLoadOlderStarted(state), new Error("nope"));
  assert.equal(state.loadingOlderMessages, false);
  assert.equal(state.olderMessagesError, "nope");
});

test("composer hint and input changes are controller state", () => {
  let result = projectComposerHint(createSessionReadState(), "Enter a message");
  assert.equal(result.state.composerHint, "Enter a message");
  assert.deepEqual(result.effects, [{ type: "scheduleComposerHintClear" }]);

  let state = clearComposerHint(result.state);
  assert.equal(state.composerHint, null);

  state = projectStartTurnFailed(state, false, new Error("failed")).state;
  assert.deepEqual(state.projection.status, { kind: "error", detail: "failed" });

  state = projectPromptInputChanged(state);
  assert.deepEqual(state.projection.status, { kind: "idle" });

  state = markSessionProjectionIdle(state);
  assert.deepEqual(state.projection.status, { kind: "idle" });
});

function session(id: string, messages: Message[]): SessionShowResponse {
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
