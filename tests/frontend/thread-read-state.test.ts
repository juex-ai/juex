import { strict as assert } from "node:assert";
import test from "node:test";

import {
  captureThreadLiveSubscription,
  clearComposerHint,
  createThreadReadState,
  projectComposerHint,
  projectLiveBrowserEvent,
  projectLoadOlderFailed,
  projectLoadOlderStarted,
  projectLoadOlderSucceeded,
  projectPendingSubmit,
  projectPromptInputChanged,
  projectThreadLoadFailed,
  projectThreadLoaded,
  projectStartTurnFailed,
  projectStartTurnSucceeded,
  resetThreadReadState,
} from "../../frontend/src/lib/thread-read-state.ts";
import type {
  BrowserEvent,
  MediaRef,
  Message,
  ThreadShowResponse,
} from "../../frontend/src/types.ts";

const imageMedia: MediaRef = {
	artifact_path: "threads/thread/media/image.png",
  media_type: "image/png",
  sha256: "abc",
};

test("resetThreadReadState clears route-local transcript state", () => {
  const state = resetThreadReadState(createThreadReadState());

  assert.equal(state.projection.messages.length, 0);
  assert.equal(state.composerHint, null);
  assert.equal(state.loadingOlderMessages, false);
});

test("live subscription keeps its bootstrap cursor across transcript refreshes", () => {
  const initial = captureThreadLiveSubscription(
    null,
    { ...thread("s1", []), event_cursor: "cursor-1" },
  );
  const refreshed = captureThreadLiveSubscription(
    initial,
    { ...thread("s1", []), event_cursor: "cursor-2" },
  );
  const switched = captureThreadLiveSubscription(
    refreshed,
    { ...thread("s2", []), event_cursor: "cursor-3" },
  );

  assert.deepEqual(initial, { threadID: "s1", cursor: "cursor-1" });
  assert.equal(refreshed, initial);
  assert.deepEqual(switched, { threadID: "s2", cursor: "cursor-3" });
});

// A thread with an empty journal reports an empty cursor. Freezing that
// placeholder made every later reconnect claim the browser had seen nothing,
// so the server replayed the whole journal.
test("live subscription adopts a real cursor once the placeholder resolves", () => {
  const bootstrap = captureThreadLiveSubscription(null, thread("s1", []));
  const resolved = captureThreadLiveSubscription(
    bootstrap,
    { ...thread("s1", []), event_cursor: "cursor-9" },
  );
  const settled = captureThreadLiveSubscription(
    resolved,
    { ...thread("s1", []), event_cursor: "cursor-20" },
  );

  assert.deepEqual(bootstrap, { threadID: "s1", cursor: "" });
  assert.deepEqual(resolved, { threadID: "s1", cursor: "cursor-9" });
  assert.equal(settled, resolved);
});

// An empty cursor still empty on refresh must keep the same reference, or the
// subscribe effect would tear down and rebuild the stream on every refresh.
test("live subscription does not churn while the cursor stays empty", () => {
  const bootstrap = captureThreadLiveSubscription(null, thread("s1", []));
  const again = captureThreadLiveSubscription(bootstrap, thread("s1", []));

  assert.equal(again, bootstrap);
});

test("thread load failure leaves loading and route reset clears stale data", () => {
  let state = projectThreadLoaded(
    createThreadReadState(),
    thread("old", []),
  );
  state = projectThreadLoadFailed(
    state,
    new Error("thread not found: missing"),
  );

  assert.equal(state.data, null);
  assert.equal(state.loadError, "thread not found: missing");
  assert.equal(state.olderMessagesError, null);

  state = projectThreadLoaded(state, thread("old", []));
  state = resetThreadReadState(state);
  assert.equal(state.data, null);
  assert.equal(state.loadError, null);
});

test("thread load failure extracts plain API error objects", () => {
  let state = projectThreadLoadFailed(createThreadReadState(), {
    message: "thread not found: object-message",
  });

  assert.equal(state.loadError, "thread not found: object-message");

  state = projectThreadLoadFailed(createThreadReadState(), {
    error: "not_found",
  });

  assert.equal(state.loadError, "not_found");
});

test("thread transient failures extract plain API error objects", () => {
  let state = projectLoadOlderFailed(createThreadReadState(), {
    message: "older page unavailable",
  });

  assert.equal(state.olderMessagesError, "older page unavailable");

  state = projectLoadOlderFailed(createThreadReadState(), {});

  assert.equal(state.olderMessagesError, "Failed to load older messages.");

  const result = projectStartTurnFailed(createThreadReadState(), false, {
    error: "turn rejected",
  });

  assert.equal(result.state.submitError, "turn rejected");

  const fallbackResult = projectStartTurnFailed(createThreadReadState(), false, {});

  assert.equal(fallbackResult.state.submitError, "Failed to start turn.");
});

test("projectLiveBrowserEvent carries projection effects through controller", () => {
  const result = projectLiveBrowserEvent(createThreadReadState(), {
    id: "evt-1",
    type: "turn.completed",
    ts: "2026-06-15T00:00:00Z",
    payload: {
      duration_ms: 10,
      output_len: 5,
    },
  });

  assert.deepEqual(result.effects, [{ type: "refresh" }]);
});

test("terminal live event refreshes the persisted transcript", () => {
  const initial = projectThreadLoaded(
    createThreadReadState(),
    thread("running", []),
  );
  const result = projectLiveBrowserEvent(initial, {
    id: "evt-error",
    type: "turn.errored",
    ts: "2026-07-20T01:00:00Z",
    turn_id: "turn-1",
    payload: { error: "cancelled", error_kind: "cancelled" },
  });

  assert.deepEqual(result.effects, [{ type: "refresh" }]);
});

test("compaction refresh reconciles persisted messages from the live projection", () => {
  let state = projectThreadLoaded(
    createThreadReadState(),
    thread("s1", []),
  );
  state = projectLiveBrowserEvent(state, {
    id: "evt-started",
    type: "turn.started",
    ts: "2026-08-14T00:00:00Z",
    turn_id: "turn-1",
    payload: {
      input: "compact before continuing",
      kind: "user",
      message_id: "msg-user",
    },
  }).state;
  const compacted = projectLiveBrowserEvent(state, {
    id: "evt-compact",
    type: "context.compact.completed",
    ts: "2026-08-14T00:00:01Z",
    turn_id: "turn-1",
    payload: {
      message_id: "msg-compact",
      reason: "auto",
      auto: true,
      estimated_tokens: 900,
      tokens_before: 900,
      tokens_after: 40,
      summary_chars: 20,
      summary_model: "gpt-test",
      context_window: 1000,
      reserve_tokens: 100,
      keep_recent_tokens: 100,
    },
  });

  assert.deepEqual(compacted.effects, [
    { type: "refresh", preserveLiveMessages: true },
  ]);
  state = projectThreadLoaded(
    compacted.state,
    thread("s1", [
      {
        id: "msg-user",
        role: "user",
        turn_id: "turn-1",
        blocks: [{ type: "text", text: "compact before continuing" }],
      },
      {
        id: "msg-compact",
        role: "user",
        kind: "compact",
        blocks: [{ type: "text", text: "summary" }],
      },
    ]),
    { preserveLiveMessages: true },
  );

  const visible = [
    ...(state.data?.messages ?? []),
    ...state.projection.messages,
  ];
  assert.equal(
    visible.filter((message) => message.id === "msg-user").length,
    1,
  );
  assert.equal(
    state.projection.messages.some(
      (message) =>
        message.role === "assistant" &&
        message.turn_id === "turn-1" &&
        message.pending,
    ),
    true,
  );

  state = projectLiveBrowserEvent(state, {
    id: "evt-tool",
    type: "tool.requested",
    ts: "2026-08-14T00:00:02Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
    },
  }).state;
  assert.equal(
    state.projection.messages.some((message) =>
      message.blocks?.some(
        (block) =>
          block.type === "tool_use" && block.tool_use_id === "tool-1",
      ),
    ),
    true,
  );
});

test("preserved refresh only reconciles exact stable message ids", () => {
  const initial = projectThreadLoaded(
    createThreadReadState(),
    thread("s1", []),
  );
  const state = {
    ...initial,
    projection: {
      ...initial.projection,
      messages: [
        {
          id: "persisted-user",
          role: "user" as const,
          turn_id: "turn-1",
          blocks: [{ type: "text" as const, text: "same input" }],
        },
        {
          id: "live-user",
          role: "user" as const,
          turn_id: "turn-2",
          blocks: [{ type: "text" as const, text: "same input" }],
        },
        {
          role: "assistant" as const,
          turn_id: "turn-2",
          pending: true,
          blocks: [
            {
              type: "tool_result" as const,
              tool_use_id: "tool-live",
              content: "still streaming",
              streaming: true,
            },
          ],
        },
      ],
    },
  };

  const refreshed = projectThreadLoaded(
    state,
    thread("s1", [
      {
        id: "persisted-user",
        role: "user",
        turn_id: "turn-1",
        blocks: [{ type: "text", text: "same input" }],
      },
    ]),
    { preserveLiveMessages: true },
  );

  assert.deepEqual(
    refreshed.projection.messages.map((message) => message.id),
    ["live-user", undefined],
  );
  assert.equal(
    refreshed.projection.messages[1]?.blocks?.[0]?.type,
    "tool_result",
  );
  assert.deepEqual(
    projectThreadLoaded(state, thread("s1", [])).projection.messages,
    [],
  );
});

test("replay skips transcript content already present in the initial thread page", () => {
  let state = projectThreadLoaded(
    createThreadReadState(),
    thread("s1", [
      {
        id: "msg-user",
        role: "user",
        blocks: [{ type: "text", text: "run command" }],
      },
      {
        id: "msg-assistant",
        role: "assistant",
        model: "gpt-test",
        blocks: [
          { type: "text", text: "done" },
          {
            type: "tool_use",
            tool_use_id: "tool-1",
            tool_name: "exec_command",
          },
        ],
      },
      {
        id: "msg-tool-result",
        role: "user",
        blocks: [
          {
            type: "tool_result",
            tool_use_id: "tool-1",
            content: "ok",
          },
        ],
      },
      {
        id: "msg-hook",
        role: "system",
        kind: "policy_event",
        blocks: [{ type: "text", text: "hook completed" }],
      },
      {
        id: "pending-message-1",
        role: "user",
        blocks: [{ type: "text", text: "queued follow-up" }],
      },
    ]),
  );

  state = projectLiveBrowserEvent(state, {
    id: "evt-started",
    type: "turn.started",
    ts: "2026-07-23T11:00:00Z",
    turn_id: "turn-1",
    payload: {
      input: "run command",
      message_id: "msg-user",
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-responded",
    type: "llm.responded",
    ts: "2026-07-23T11:00:01Z",
    turn_id: "turn-1",
    payload: {
      message_id: "msg-assistant",
      stop_reason: "tool_use",
      usage: { input_tokens: 1, output_tokens: 1 },
      token_usage: { input_tokens: 1, output_tokens: 1 },
      blocks: [{ type: "text", text: "done" }],
      text: "done",
      thinking: "",
      tool_calls: [],
      model: "gpt-test",
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-tool-requested",
    type: "tool.requested",
    ts: "2026-07-23T11:00:02Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      timeout_seconds: 30,
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-tool-completed",
    type: "tool.completed",
    ts: "2026-07-23T11:00:03Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      timeout_seconds: 30,
      len: 2,
      preview: "ok",
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-tool-outcome-unknown",
    type: "tool.outcome_unknown",
    ts: "2026-07-23T11:00:03Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      iter: 0,
      call_index: 0,
      message_id: "msg-assistant",
      error: "TOOL_OUTCOME_UNKNOWN",
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-transcript-repaired",
    type: "transcript.repaired",
    ts: "2026-07-23T11:00:03Z",
    payload: {
      reason: "load",
      repairs: [{
        tool_use_id: "tool-1",
        tool_name: "exec_command",
        repair_message_id: "msg-tool-result",
        provider_iteration: 0,
        call_index: 0,
        assistant_message_id: "msg-assistant",
        execution_phase: "started",
        recovery_code: "TOOL_OUTCOME_UNKNOWN",
      }],
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-hook-trace",
    type: "policy.trace",
    ts: "2026-07-23T11:00:04Z",
    turn_id: "turn-1",
    payload: {
      text: "hook completed",
      message_id: "msg-hook",
    },
  }).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-pending-queued",
    type: "pending_input.queued",
    ts: "2026-07-23T11:00:05Z",
    turn_id: "turn-1",
    payload: {
      input: "queued follow-up",
      kind: "user",
      message_id: "pending-message-1",
      pending_count: 1,
      max_pending_inputs: 16,
    },
  }).state;

  assert.deepEqual(state.projection.messages, []);
  assert.deepEqual(state.projection.queuedInput.items, []);
});

test("reconnect replay skips transcript content already projected live", () => {
  let state = projectThreadLoaded(
    createThreadReadState(),
    thread("s1", []),
  );
  const liveEvents: BrowserEvent[] = [
    {
      id: "evt-started",
      type: "turn.started",
      ts: "2026-07-23T14:00:00Z",
      turn_id: "turn-1",
      payload: {
        input: "run command",
        kind: "user",
        message_id: "msg-user",
      },
    },
    {
      id: "evt-responded",
      type: "llm.responded",
      ts: "2026-07-23T14:00:01Z",
      turn_id: "turn-1",
      payload: {
        message_id: "msg-assistant",
        stop_reason: "tool_use",
        usage: { input_tokens: 1, output_tokens: 1 },
        token_usage: { input_tokens: 1, output_tokens: 1 },
        blocks: [{ type: "text", text: "done" }],
        text: "done",
        thinking: "",
        tool_calls: [],
        model: "gpt-test",
      },
    },
    {
      id: "evt-tool-requested",
      type: "tool.requested",
      ts: "2026-07-23T14:00:02Z",
      turn_id: "turn-1",
      payload: {
        name: "exec_command",
        tool_use_id: "tool-1",
        timeout_seconds: 30,
      },
    },
    {
      id: "evt-tool-completed",
      type: "tool.completed",
      ts: "2026-07-23T14:00:03Z",
      turn_id: "turn-1",
      payload: {
        name: "exec_command",
        tool_use_id: "tool-1",
        timeout_seconds: 30,
        len: 2,
        preview: "ok",
      },
    },
    {
      id: "evt-hook-trace",
      type: "policy.trace",
      ts: "2026-07-23T14:00:04Z",
      turn_id: "turn-1",
      payload: {
        text: "hook completed",
        message_id: "msg-hook",
      },
    },
  ];
  for (const event of liveEvents) {
    state = projectLiveBrowserEvent(state, event).state;
  }

  const beforeReplay = structuredClone(state.projection);
  for (const event of liveEvents) {
    state = projectLiveBrowserEvent(state, event).state;
  }
  assert.deepEqual(state.projection, beforeReplay);

  const queuedEvent: BrowserEvent = {
    id: "evt-pending-queued",
    type: "pending_input.queued",
    ts: "2026-07-23T14:00:05Z",
    turn_id: "turn-1",
    payload: {
      input: "queued follow-up",
      kind: "user",
      message_id: "msg-pending",
      pending_count: 1,
      max_pending_inputs: 16,
    },
  };
  state = projectLiveBrowserEvent(state, queuedEvent).state;
  state = projectLiveBrowserEvent(state, {
    id: "evt-pending-draining",
    type: "pending_input.draining",
    ts: "2026-07-23T14:00:06Z",
    turn_id: "turn-1",
    payload: {
      count: 1,
      pending_count: 0,
      max_pending_inputs: 16,
    },
  }).state;

  const beforeQueuedReplay = structuredClone(state.projection);
  state = projectLiveBrowserEvent(state, queuedEvent).state;
  assert.deepEqual(state.projection, beforeQueuedReplay);
});

test("projectLiveBrowserEvent refreshes thread goal state", () => {
  const initial = projectThreadLoaded(
    createThreadReadState(),
    {
      ...thread("s1", []),
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
});

test("projectLiveBrowserEvent refreshes thread notes", () => {
  const initial = projectThreadLoaded(createThreadReadState(), {
    ...thread("s1", []),
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
  let state = createThreadReadState();
  let result = projectStartTurnSucceeded(
    state,
    "second prompt",
    {
      queued: true,
      pending_count: 1,
    },
    [],
    "2026-06-15T00:00:00Z",
  );
  state = result.state;
  assert.equal(state.projection.queuedInput.items.length, 1);
  assert.equal(
    state.projection.queuedInput.items[0]?.createdAt,
    "2026-06-15T00:00:00Z",
  );

  result = projectStartTurnSucceeded(
    state,
    "new prompt",
    { turn_id: "turn-2" },
    [],
    "2026-06-15T00:00:01Z",
  );
  assert.equal(result.state.projection.messages.at(-2)?.turn_id, "turn-2");
  assert.equal(
    result.state.projection.messages.at(-2)?.created_at,
    "2026-06-15T00:00:01Z",
  );
  assert.deepEqual(result.effects, []);
});

test("projectStartTurnSucceeded records optimistic image attachments", () => {
  const result = projectStartTurnSucceeded(
    createThreadReadState(),
    "",
    { turn_id: "turn-image" },
    [imageMedia],
  );

  const user = result.state.projection.messages.at(-2);
  assert.deepEqual(user?.blocks, [{ type: "image", media: imageMedia }]);
});

test("projectStartTurnSucceeded surfaces attachment capability warnings", () => {
  const result = projectStartTurnSucceeded(
    createThreadReadState(),
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

test("projectStartTurnSucceeded refreshes the current Thread for /new", () => {
  const result = projectStartTurnSucceeded(createThreadReadState(), "/new", {
    turn_id: "turn-new",
    command: {
      name: "/new",
      text: "Created new thread",
      status: { thread_id: "0" },
    },
  });

  assert.deepEqual(result.effects, [
    { type: "refresh", preserveLiveMessages: true },
  ]);
});

test("projectStartTurnSucceeded preserves compact command submission time", () => {
  let state = projectPendingSubmit(
    createThreadReadState(),
    "/compact",
    "2026-08-20T20:30:00Z",
  );
  assert.equal(state.projection.messages.at(-1)?.pending, true);
  assert.equal(
    state.projection.messages[0]?.created_at,
    "2026-08-20T20:30:00Z",
  );

  const result = projectStartTurnSucceeded(
    state,
    "/compact",
    {
      command: {
        name: "/compact",
        text: "Compacted",
        compact: { message_id: "compact-1" },
      },
    },
    [],
    "2026-08-20T20:30:00Z",
  );

  assert.deepEqual(result.state.projection.compactCommands["compact-1"], {
    input: "/compact",
    submittedAt: "2026-08-20T20:30:00Z",
  });
  assert.deepEqual(result.effects, [
    { type: "refresh", preserveLiveMessages: true },
  ]);
});

test("projectStartTurnSucceeded preserves non-compact command submission time", () => {
  const result = projectStartTurnSucceeded(
    createThreadReadState(),
    "/status",
    {
      command: {
        name: "/status",
        text: "ok",
      },
    },
    [],
    "2026-08-20T21:05:00Z",
  );

  assert.equal(
    result.state.projection.messages[0]?.created_at,
    "2026-08-20T21:05:00Z",
  );
  assert.equal(result.state.projection.messages[1]?.created_at, undefined);
});

test("load older state merges pages and records errors", () => {
  const base = projectThreadLoaded(
    createThreadReadState(),
    thread("s1", [{ role: "user", blocks: [{ type: "text", text: "new" }] }]),
    { preserveLiveMessages: true },
  );

  let state = projectLoadOlderStarted(base);
  assert.equal(state.loadingOlderMessages, true);

  state = projectLoadOlderSucceeded(
    state,
    thread("s1", [{ role: "user", blocks: [{ type: "text", text: "old" }] }]),
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

test("full thread load settles stale older-message loading state", () => {
  let state = projectThreadLoaded(
    createThreadReadState(),
    thread("s1", [{ role: "user", blocks: [{ type: "text", text: "new" }] }]),
    { preserveLiveMessages: true },
  );

  state = projectLoadOlderFailed(
    projectLoadOlderStarted(state),
    new Error("older page failed"),
  );
  state = projectLoadOlderStarted(state);

  state = projectThreadLoaded(
    state,
    thread("s1", [{ role: "user", blocks: [{ type: "text", text: "fresh" }] }]),
    { preserveLiveMessages: true },
  );

  assert.equal(state.loadingOlderMessages, false);
  assert.equal(state.olderMessagesError, null);
});

test("composer hint and input changes are controller state", () => {
  let result = projectComposerHint(createThreadReadState(), "Enter a message");
  assert.equal(result.state.composerHint, "Enter a message");
  assert.deepEqual(result.effects, [{ type: "scheduleComposerHintClear" }]);

  let state = clearComposerHint(result.state);
  assert.equal(state.composerHint, null);

  state = projectStartTurnFailed(state, false, new Error("failed")).state;
  assert.equal(state.submitError, "failed");

  state = projectPromptInputChanged(state);
  assert.equal(state.submitError, null);
});

function thread(id: string, messages: Message[]): ThreadShowResponse {
  return {
    id,
    dir: `/tmp/${id}`,
    kind: "primary",
    active: true,
    started_at: "2026-06-15T00:00:00Z",
    last_active_at: "2026-06-15T00:00:00Z",
    turns: 1,
    preview: "preview",
    token_usage: { total: { input_tokens: 1, output_tokens: 1 }, by_model: {} },
    event_cursor: "",
    messages,
  };
}
