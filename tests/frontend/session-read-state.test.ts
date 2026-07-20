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
  resetSessionReadState,
} from "../../frontend/src/lib/session-read-state.ts";
import type {
  MediaRef,
  Message,
  SessionShowResponse,
} from "../../frontend/src/types.ts";

const imageMedia: MediaRef = {
  artifact_path: ".juex/artifacts/media/session/image.png",
  media_type: "image/png",
  sha256: "abc",
};

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

test("session load failure extracts plain API error objects", () => {
  let state = projectSessionLoadFailed(createSessionReadState(), {
    message: "session not found: object-message",
  });

  assert.equal(state.loadError, "session not found: object-message");

  state = projectSessionLoadFailed(createSessionReadState(), {
    error: "not_found",
  });

  assert.equal(state.loadError, "not_found");
});

test("session transient failures extract plain API error objects", () => {
  let state = projectLoadOlderFailed(createSessionReadState(), {
    message: "older page unavailable",
  });

  assert.equal(state.olderMessagesError, "older page unavailable");

  state = projectLoadOlderFailed(createSessionReadState(), {});

  assert.equal(state.olderMessagesError, "Failed to load older messages.");

  const result = projectStartTurnFailed(createSessionReadState(), false, {
    error: "turn rejected",
  });

  assert.deepEqual(result.state.projection.status, {
    kind: "error",
    detail: "turn rejected",
  });
  assert.equal(result.state.submitError, "turn rejected");

  const fallbackResult = projectStartTurnFailed(createSessionReadState(), false, {});

  assert.deepEqual(fallbackResult.state.projection.status, {
    kind: "error",
    detail: "Failed to start turn.",
  });
  assert.equal(fallbackResult.state.submitError, "Failed to start turn.");
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

test("terminal live event settles the optimistic active turn", () => {
  const initial = resetSessionReadState(
    projectSessionLoaded(createSessionReadState(), session("running", [])),
    { activeTurnID: "turn-1" },
  );

  assert.equal(initial.projection.activeTurnID, "turn-1");
  const result = projectLiveBrowserEvent(initial, {
    id: "evt-error",
    type: "turn.errored",
    ts: "2026-07-20T01:00:00Z",
    turn_id: "turn-1",
    payload: { error: "cancelled", error_kind: "cancelled" },
  });

  assert.equal(result.state.projection.turnActive, false);
  assert.equal(result.state.projection.activeTurnID, null);
  assert.equal(result.state.projection.settledTurnID, "turn-1");
  assert.deepEqual(result.effects, [{ type: "refresh" }]);
});

test("projectLiveBrowserEvent refreshes session goal state", () => {
  const initial = projectSessionLoaded(
    createSessionReadState(),
    {
      ...session("s1", []),
      goal: {
        status: "in_progress",
        description: "old goal",
        acceptance: "old checks",
        continuation_count: 7,
        updated_at: "2026-06-15T00:00:00Z",
      },
    },
  );

  const result = projectLiveBrowserEvent(initial, {
    id: "evt-goal",
    type: "goal.updated",
    ts: "2026-06-15T00:01:00Z",
    turn_id: "turn-1",
    payload: {
      status: "success",
      description: "new goal",
      updated_at: "2026-06-15T00:01:00Z",
    },
  });

  assert.deepEqual(result.state.data?.goal, {
    status: "success",
    description: "new goal",
    updated_at: "2026-06-15T00:01:00Z",
  });
  assert.equal(result.state.data?.id, "s1");
  assert.deepEqual(result.effects, []);
  assert.deepEqual(result.state.projection.status, { kind: "idle" });
});

test("projectLiveBrowserEvent refreshes session notes", () => {
  const initial = projectSessionLoaded(createSessionReadState(), {
    ...session("s1", []),
    notes: {
      content: "- [ ] old task",
      updated_at: "2026-06-15T00:00:00Z",
    },
  });

  const result = projectLiveBrowserEvent(initial, {
    id: "evt-notes",
    type: "notes.updated",
    ts: "2026-06-15T00:01:00Z",
    turn_id: "turn-1",
    payload: {
      content: "- [x] old task\n- [ ] next task",
      updated_at: "2026-06-15T00:01:00Z",
    },
  });

  assert.deepEqual(result.state.data?.notes, {
    content: "- [x] old task\n- [ ] next task",
    updated_at: "2026-06-15T00:01:00Z",
  });
  assert.equal(result.state.data?.id, "s1");
  assert.deepEqual(result.effects, []);
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

test("projectStartTurnSucceeded records optimistic image attachments", () => {
  const result = projectStartTurnSucceeded(
    createSessionReadState(),
    "",
    { turn_id: "turn-image" },
    [imageMedia],
  );

  const user = result.state.projection.messages.at(-2);
  assert.deepEqual(user?.blocks, [{ type: "image", media: imageMedia }]);
  assert.equal(result.state.projection.turnActive, true);
});

test("projectStartTurnSucceeded surfaces attachment capability warnings", () => {
  const result = projectStartTurnSucceeded(
    createSessionReadState(),
    "describe this",
    {
      turn_id: "turn-warning",
      warnings: [
        {
          code: "attachment_vision_unavailable",
          message: 'model "ark-anthropic:minimax-m3" cannot view attached image content',
          suggestion:
            "use a vision-capable model or configure providers[].models[].capabilities.vision",
        },
      ],
    },
    [imageMedia],
  );

  assert.equal(
    result.state.composerHint,
    'Warning: model "ark-anthropic:minimax-m3" cannot view attached image content; use a vision-capable model or configure providers[].models[].capabilities.vision',
  );
  assert.deepEqual(result.effects, [{ type: "scheduleComposerHintClear" }]);
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

test("full session load settles stale older-message loading state", () => {
  let state = projectSessionLoaded(
    createSessionReadState(),
    session("s1", [{ role: "user", blocks: [{ type: "text", text: "new" }] }]),
    { preserveLiveMessages: true },
  );

  state = projectLoadOlderFailed(
    projectLoadOlderStarted(state),
    new Error("older page failed"),
  );
  state = projectLoadOlderStarted(state);

  state = projectSessionLoaded(
    state,
    session("s1", [{ role: "user", blocks: [{ type: "text", text: "fresh" }] }]),
    { preserveLiveMessages: true },
  );

  assert.equal(state.loadingOlderMessages, false);
  assert.equal(state.olderMessagesError, null);
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
  assert.equal(state.submitError, null);

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
