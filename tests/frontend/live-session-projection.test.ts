import { strict as assert } from "node:assert";
import test from "node:test";

import {
  createLiveSessionProjection,
  projectLiveSessionEvent,
  projectOptimisticTurn,
  projectQueuedInput,
  projectSessionTurnStatus,
  type LiveSessionProjection,
} from "../../frontend/src/lib/live-session-projection.ts";
import { messagesToGroups } from "../../frontend/src/lib/display-units.ts";
import type { BrowserEvent, MediaRef } from "../../frontend/src/types.ts";

const imageMedia: MediaRef = {
  artifact_path: ".juex/artifacts/media/session/image.png",
  media_type: "image/png",
  sha256: "abc",
};

test("projectLiveSessionEvent projects a live turn with tool deltas and completion effects", () => {
  let state = createLiveSessionProjection();

  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "run command" },
  });
  assert.equal(state.turnActive, true);
  assert.equal(state.messages.length, 2);

  state = apply(state, {
    id: "e2",
    type: "llm.responded",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: {
      stop_reason: "tool_use",
      usage: { input_tokens: 10, output_tokens: 5 },
      token_usage: { input_tokens: 10, output_tokens: 5 },
      blocks: [{ type: "text", text: "I'll run it." }],
      text: "I'll run it.",
      thinking: "",
      tool_calls: [],
      model: "gpt-test",
      context_usage: {
        input_tokens: 10,
        output_tokens: 5,
        total_tokens: 15,
      },
    },
  });
  assert.deepEqual(state.tokenUsage, { input_tokens: 10, output_tokens: 5 });
  assert.equal(state.contextUsage?.total_tokens, 15);
  assert.equal(state.messages[1].pending, false);

  state = apply(state, {
    id: "e3",
    type: "tool.requested",
    ts: "2026-06-15T00:00:02Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      input: { cmd: "printf hi" },
      timeout_seconds: 30,
    },
  });
  assert.deepEqual(state.status, { kind: "tool", name: "exec_command" });

  state = apply(state, {
    id: "e4",
    type: "tool.output_delta",
    ts: "2026-06-15T00:00:03Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      session_id: "shell-1",
      chunk_id: 1,
      stream: "stdout",
      text: "hi",
      truncated: true,
    },
  });
  let liveResult = state.messages.at(-1)?.blocks?.[0];
  assert.equal(liveResult?.type, "tool_result");
  if (liveResult?.type === "tool_result") {
    assert.equal(liveResult.content, "hi");
  }

  state = apply(state, {
    id: "e5",
    type: "tool.completed",
    ts: "2026-06-15T00:00:04Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      timeout_seconds: 30,
      len: 2,
      preview: "hi",
    },
  });
  assert.deepEqual(state.status, { kind: "running" });
  liveResult = state.messages.at(-1)?.blocks?.[0];
  if (liveResult?.type === "tool_result") {
    assert.equal(liveResult.content, "hi");
  }
  assert.equal(
    state.messages
      .at(-1)
      ?.blocks?.filter(
        (block) =>
          block.type === "tool_result" && block.tool_use_id === "tool-1",
      ).length,
    1,
  );

  const completed = projectLiveSessionEvent(state, {
    id: "e6",
    type: "turn.completed",
    ts: "2026-06-15T00:00:05Z",
    turn_id: "turn-1",
    payload: {
      duration_ms: 5000,
      output_len: 2,
      token_usage: { input_tokens: 10, output_tokens: 5 },
    },
  });
  assert.equal(completed.state.turnActive, false);
  assert.deepEqual(completed.state.status, { kind: "done" });
  assert.deepEqual(completed.effects, [
    { type: "refresh" },
    { type: "scheduleIdleStatus" },
  ]);
});

test("projectLiveSessionEvent accumulates LLM deltas and reconciles the final response", () => {
  let state = createLiveSessionProjection();
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-stream",
    payload: { input: "stream" },
  });
  state = apply(state, {
    id: "e2",
    type: "llm.output_delta",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-stream",
    payload: { iter: 0, model: "gpt-test", kind: "reasoning", index: 0, text: "plan " },
  });
  state = apply(state, {
    id: "e3",
    type: "llm.output_delta",
    ts: "2026-06-15T00:00:02Z",
    turn_id: "turn-stream",
    payload: { iter: 0, model: "gpt-test", kind: "text", index: 1, text: "hel" },
  });
  state = apply(state, {
    id: "e4",
    type: "llm.output_delta",
    ts: "2026-06-15T00:00:03Z",
    turn_id: "turn-stream",
    payload: { iter: 0, model: "gpt-test", kind: "text", index: 1, text: "lo" },
  });

  const streaming = state.messages[1];
  assert.equal(streaming.pending, true);
  assert.equal(streaming.model, "gpt-test");
  assert.deepEqual(streaming.blocks, [
    { type: "reasoning", text: "plan ", stream_index: 0 },
    { type: "text", text: "hello", stream_index: 1 },
  ]);

  state = apply(state, {
    id: "e-retry",
    type: "llm.retry",
    ts: "2026-06-15T00:00:03.500Z",
    turn_id: "turn-stream",
    payload: {
      purpose: "turn",
      provider: "openai-codex",
      model: "gpt-test",
      operation: "responses.sse",
      attempt: 1,
      max_attempts: 2,
      retry_reason: "codex_sse_idle_timeout",
      will_retry: true,
    },
  });
  assert.equal(state.messages[1].pending, true);
  assert.deepEqual(state.messages[1].blocks, []);

  state = apply(state, {
    id: "e5",
    type: "llm.responded",
    ts: "2026-06-15T00:00:04Z",
    turn_id: "turn-stream",
    payload: {
      stop_reason: "end_turn",
      usage: { input_tokens: 5, output_tokens: 2 },
      blocks: [
        { type: "reasoning", text: "plan final" },
        { type: "text", text: "hello final" },
      ],
      text: "hello final",
      thinking: "plan final",
      tool_calls: [],
      model: "gpt-test",
    },
  });

  const reconciled = state.messages[1];
  assert.equal(reconciled.pending, false);
  assert.deepEqual(reconciled.blocks, [
    { type: "reasoning", text: "plan final" },
    { type: "text", text: "hello final" },
  ]);
});

test("projectLiveSessionEvent inserts a fallback notice before its assistant", () => {
  let state = createLiveSessionProjection();
  state = apply(state, {
    id: "start-fallback",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-fallback",
    payload: { input: "continue" },
  });
  state = apply(state, {
    id: "respond-fallback",
    type: "llm.responded",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-fallback",
    payload: {
      stop_reason: "end_turn",
      usage: { input_tokens: 5, output_tokens: 2 },
      token_usage: { input_tokens: 5, output_tokens: 2 },
      blocks: [{ type: "text", text: "continued" }],
      text: "continued",
      thinking: "",
      tool_calls: [],
      model: "backup:model",
      notice: {
        role: "user",
        kind: "model_fallback",
        blocks: [{ type: "text", text: "model switched" }],
      },
    },
  });

  assert.equal(state.messages.length, 3);
  assert.equal(state.messages[1].kind, "model_fallback");
  assert.equal(state.messages[2].role, "assistant");
  assert.equal(state.messages[2].model, "backup:model");
});

test("projectLiveSessionEvent starts a new streamed assistant for the next provider iteration", () => {
  let state: LiveSessionProjection = {
    ...createLiveSessionProjection(),
    messages: [
      {
        role: "assistant",
        turn_id: "turn-stream",
        pending: false,
        blocks: [{ type: "text", text: "calling tool" }],
      },
    ],
  };

  state = apply(state, {
    id: "e-next",
    type: "llm.output_delta",
    ts: "2026-06-15T00:00:05Z",
    turn_id: "turn-stream",
    payload: { iter: 1, model: "gpt-test", kind: "text", index: 0, text: "final" },
  });

  assert.equal(state.messages.length, 2);
  assert.equal(state.messages[0].pending, false);
  assert.equal(state.messages[1].pending, true);
  assert.deepEqual(state.messages[1].blocks, [
    { type: "text", text: "final", stream_index: 0 },
  ]);
});

test("projectLiveSessionEvent keeps streamed output paired when requested was missed", () => {
  let state = createLiveSessionProjection();

  state = apply(state, {
    id: "e1",
    type: "tool.output_delta",
    ts: "2026-06-15T00:00:03Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      session_id: "shell-1",
      chunk_id: 1,
      stream: "stdout",
      text: "pulling layer\n",
    },
  });

  const groups = messagesToGroups(state.messages);
  assert.equal(groups.length, 1);
  assert.equal(groups[0].role, "assistant");
  const unit = groups[0].units[0];
  assert.equal(unit?.kind, "tool");
  if (unit?.kind === "tool") {
    assert.equal(unit.use?.tool_name, "exec_command");
    assert.equal(unit.result?.content, "pulling layer\n");
  }
});

test("projectOptimisticTurn is replaced by the canonical turn.started event", () => {
  let state = createLiveSessionProjection();
  state = projectOptimisticTurn(state, "local-turn", "hello");

  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "canonical-turn",
    payload: { input: "hello" },
  });
  assert.equal(state.messages.length, 2);
  assert.deepEqual(
    state.messages.map((message) => message.turn_id),
    ["canonical-turn", "canonical-turn"],
  );

  state = apply(state, {
    id: "e1-replayed",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "canonical-turn",
    payload: { input: "hello" },
  });
  assert.equal(state.messages.length, 2);
});

test("projectOptimisticTurn renders attachment-only image blocks", () => {
  const state = projectOptimisticTurn(
    createLiveSessionProjection(),
    "turn-image",
    "",
    undefined,
    [imageMedia],
  );

  assert.equal(state.messages.length, 2);
  assert.deepEqual(state.messages[0].blocks, [
    { type: "image", media: imageMedia },
  ]);
  assert.equal(state.messages[1].role, "assistant");
  assert.equal(state.messages[1].pending, true);
});

test("projectQueuedInput keeps image attachments for drained queued turns", () => {
  let state = createLiveSessionProjection();
  state = projectQueuedInput(state, "", undefined, 1, [imageMedia]);
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "active" },
  });
  state = apply(state, {
    id: "e2",
    type: "pending_input.drained",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: { count: 1 },
  });

  const queued = state.messages.find(
    (message) => message.kind === "pending_input",
  );
  assert.deepEqual(queued?.blocks, [{ type: "image", media: imageMedia }]);
});

test("projectLiveSessionEvent preserves queued attachments when SSE omits them", () => {
  let state = createLiveSessionProjection();
  state = projectQueuedInput(state, "queued image", undefined, 1, [imageMedia]);
  state = apply(state, {
    id: "e1",
    type: "pending_input.queued",
    ts: "2026-06-15T00:00:00Z",
    payload: { input: "queued image", pending_count: 1 },
  });

  assert.deepEqual(state.queuedInput.items[0]?.attachments, [imageMedia]);
});

test("projectLiveSessionEvent promotes image-only queued turns with attachments", () => {
  let state = createLiveSessionProjection();
  state = projectQueuedInput(state, "", undefined, 1, [imageMedia]);
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-image",
    payload: { input: "" },
  });

  assert.equal(state.queuedInput.items.length, 0);
  assert.deepEqual(state.messages[0]?.blocks, [
    { type: "image", media: imageMedia },
  ]);
  assert.equal(state.messages[1]?.role, "assistant");
  assert.equal(state.messages[1]?.pending, true);
});

test("projectSessionTurnStatus does not duplicate an existing assistant turn", () => {
  const state = projectSessionTurnStatus(
    {
      ...createLiveSessionProjection(),
      messages: [
        {
          role: "assistant",
          turn_id: "turn-1",
          pending: false,
          blocks: [{ type: "text", text: "partial answer" }],
        },
      ],
    },
    { turn_id: "turn-1", state: "running" },
  );

  assert.equal(state.turnActive, true);
  assert.equal(
    state.messages.filter(
      (message) => message.role === "assistant" && message.turn_id === "turn-1",
    ).length,
    1,
  );
});

test("projectLiveSessionEvent drains queued input before the pending assistant placeholder", () => {
  let state = createLiveSessionProjection();
  state = projectQueuedInput(state, "queued follow-up", undefined, 1);
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "first prompt" },
  });
  state = apply(state, {
    id: "e2",
    type: "pending_input.drained",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: { count: 1, pending_count: 0, max_pending_inputs: 4 },
  });

  assert.equal(state.queuedInput.items.length, 0);
  assert.deepEqual(
    state.messages.map((message) => ({
      role: message.role,
      text:
        message.blocks?.[0]?.type === "text"
          ? message.blocks[0].text
          : undefined,
      pending: message.pending,
    })),
    [
      { role: "user", text: "first prompt", pending: undefined },
      { role: "user", text: "queued follow-up", pending: undefined },
      { role: "assistant", text: undefined, pending: true },
    ],
  );
});

test("projectLiveSessionEvent keeps newly queued input out of an announced drain", () => {
  let state = createLiveSessionProjection();
  state = projectQueuedInput(
    state,
    "draining follow-up",
    undefined,
    1,
    [imageMedia],
  );
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "active prompt" },
  });
  state = apply(state, {
    id: "e2",
    type: "pending_input.draining",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: { count: 1, pending_count: 0, max_pending_inputs: 4 },
  });
  state = apply(state, {
    id: "e3",
    type: "pending_input.queued",
    ts: "2026-06-15T00:00:02Z",
    turn_id: "turn-1",
    payload: {
      input: "new queued follow-up",
      kind: "",
      pending_count: 1,
      max_pending_inputs: 4,
    },
  });
  state = apply(state, {
    id: "e4",
    type: "pending_input.drained",
    ts: "2026-06-15T00:00:03Z",
    turn_id: "turn-1",
    payload: { count: 1, pending_count: 1, max_pending_inputs: 4 },
  });

  assert.deepEqual(
    state.queuedInput.items.map((item) => item.input),
    ["new queued follow-up"],
  );
  const drained = state.messages.find(
    (message) => message.kind === "pending_input",
  );
  assert.deepEqual(drained?.blocks, [
    { type: "text", text: "draining follow-up" },
    { type: "image", media: imageMedia },
  ]);
  assert.equal(
    state.messages.some(
      (message) =>
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text === "new queued follow-up",
    ),
    false,
  );
});

test("projectLiveSessionEvent keeps drained input visible after the turn errors", () => {
  let state = createLiveSessionProjection();
  state = projectQueuedInput(state, "paused follow-up", undefined, 1);
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "active" },
  });
  state = apply(state, {
    id: "e2",
    type: "pending_input.drained",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: { count: 1, pending_count: 0, max_pending_inputs: 4 },
  });
  state = apply(state, {
    id: "e3",
    type: "turn.errored",
    ts: "2026-06-15T00:00:02Z",
    turn_id: "turn-1",
    payload: { error: "cancelled by user", error_kind: "cancelled" },
  });

  assert.equal(state.queuedInput.items.length, 0);
  assert.equal(state.turnActive, false);
  assert.deepEqual(state.status, { kind: "error", detail: "cancelled by user" });
  assert.equal(
    state.messages.some(
      (message) =>
        message.role === "user" &&
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text === "paused follow-up",
    ),
    true,
  );
});

test("projectLiveSessionEvent projects compact start and completion", () => {
  let state = createLiveSessionProjection();
  state = apply(state, {
    id: "e1",
    type: "context.compact.started",
    ts: "2026-06-15T00:00:00Z",
    payload: {
      reason: "manual",
      auto: false,
      estimated_tokens: 100,
      tokens_before: 100,
      context_window: 1000,
      reserve_tokens: 100,
      keep_recent_tokens: 100,
      tail_turns: 2,
    },
  });
  assert.equal(state.compactActive, true);
  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0].pending, true);
  state = apply(state, {
    id: "e-retry",
    type: "llm.retry",
    ts: "2026-06-15T00:00:00.500Z",
    payload: {
      purpose: "compaction",
      provider: "openai-codex",
      model: "gpt-5.5",
      operation: "responses.sse",
      attempt: 1,
      max_attempts: 11,
      retry_reason: "codex_sse_read",
      will_retry: true,
    },
  });
  assert.equal(state.compactActive, true);
  assert.equal(state.turnActive, false);

  state = apply(state, {
    id: "e-summary-retry",
    type: "context.compact.summary_retry",
    ts: "2026-06-15T00:00:00.750Z",
    payload: {
      attempt: 2,
      reason: "empty_summary",
      stop_reason: "max_tokens",
      reasoning_only: true,
      previous_max_output_tokens: 2048,
      max_output_tokens: 4096,
    },
  });
  assert.equal(state.compactActive, true);
  assert.equal(state.turnActive, false);

  const completed = projectLiveSessionEvent(state, {
    id: "e2",
    type: "context.compact.completed",
    ts: "2026-06-15T00:00:01Z",
    payload: {
      message_id: "compact-1",
      reason: "manual",
      auto: false,
      estimated_tokens: 100,
      tokens_before: 100,
      tokens_after: 40,
      summary_chars: 20,
      summary_model: "gpt-test",
      tail_start_message_id: "m-tail",
      context_window: 1000,
      reserve_tokens: 100,
      keep_recent_tokens: 100,
    },
  });
  assert.equal(completed.state.compactActive, false);
  assert.equal(completed.state.messages.length, 0);
  assert.deepEqual(completed.state.status, { kind: "done" });
  assert.deepEqual(completed.effects, [
    { type: "refresh", preserveLiveMessages: true },
    { type: "scheduleIdleStatus" },
  ]);
});

test("projectLiveSessionEvent keeps duplicate tool.requested and queue drops stable", () => {
  let state = createLiveSessionProjection();
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "run command" },
  });

  const toolRequested: BrowserEvent = {
    id: "e2",
    type: "tool.requested",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      input: { cmd: "echo hi" },
      timeout_seconds: 30,
    },
  };
  state = apply(apply(state, toolRequested), toolRequested);
  const assistantBlocks = state.messages.find(
    (message) => message.role === "assistant",
  )?.blocks;
  assert.equal(
    assistantBlocks?.filter((block) => block.type === "tool_use").length,
    1,
  );

  state = projectQueuedInput(state, "queued", undefined, 1);
  state = apply(state, {
    id: "e3",
    type: "pending_input.dropped",
    ts: "2026-06-15T00:00:02Z",
    payload: { count: 1, pending_count: 0, max_pending_inputs: 4 },
  });
  state = apply(state, {
    id: "e3-replayed",
    type: "pending_input.dropped",
    ts: "2026-06-15T00:00:02Z",
    payload: { count: 1, pending_count: 0, max_pending_inputs: 4 },
  });
  assert.equal(state.queuedInput.items.length, 0);
  assert.deepEqual(state.status, {
    kind: "error",
    detail: "1 pending input(s) dropped",
  });
});

test("projectLiveSessionEvent uses pending_input.rejected reason", () => {
  const state = apply(createLiveSessionProjection(), {
    id: "e1",
    type: "pending_input.rejected",
    ts: "2026-06-15T00:00:00Z",
    payload: {
      input: "queued",
      kind: "user",
      pending_count: 4,
      max_pending_inputs: 4,
      reason: "queue disabled for this session",
    },
  });

  assert.equal(state.turnActive, true);
  assert.deepEqual(state.status, {
    kind: "error",
    detail: "queue disabled for this session",
  });
});

test("projectLiveSessionEvent accepts errored tool contract fields", () => {
  let state = createLiveSessionProjection();
  state = apply(state, {
    id: "e1",
    type: "turn.started",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: { input: "run failing command" },
  });
  state = apply(state, {
    id: "e2",
    type: "tool.requested",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      input: { cmd: "exit 7" },
      timeout_seconds: 5,
    },
  });
  state = apply(state, {
    id: "e3",
    type: "tool.errored",
    ts: "2026-06-15T00:00:02Z",
    turn_id: "turn-1",
    payload: {
      name: "exec_command",
      tool_use_id: "tool-1",
      error: "exit status 7",
      timeout_seconds: 5,
      len: 14,
      preview: "partial output",
      timed_out: false,
      exit_code: 7,
    },
  });

  const result = state.messages.at(-1)?.blocks?.at(-1);
  assert.equal(result?.type, "tool_result");
  assert.equal(result?.tool_use_id, "tool-1");
  assert.equal(result?.content, "exit status 7");
  assert.equal(result?.is_error, true);
  assert.deepEqual(state.status, { kind: "running" });
});

test("projectLiveSessionEvent projects hook.trace as weak messages", () => {
  let state = createLiveSessionProjection();
  state = apply(state, {
    id: "hook-1",
    type: "hook.trace",
    ts: "2026-06-15T00:00:00Z",
    turn_id: "turn-1",
    payload: {
      text: "hook extract-state allow UserPromptSubmit in 12ms",
    },
  });

  assert.equal(state.messages.length, 1);
  assert.equal(state.messages[0].kind, "hook_event");
  assert.equal(state.messages[0].role, "system");
  assert.equal(state.messages[0].blocks?.[0]?.type, "text");
  if (state.messages[0].blocks?.[0]?.type === "text") {
    assert.equal(
      state.messages[0].blocks[0].text,
      "hook extract-state allow UserPromptSubmit in 12ms",
    );
  }

  state = apply(state, {
    id: "hook-1",
    type: "hook.trace",
    ts: "2026-06-15T00:00:01Z",
    turn_id: "turn-1",
    payload: {
      text: "hook extract-state allow UserPromptSubmit in 12ms",
    },
  });
  assert.equal(state.messages.length, 1);
});

function apply(state: LiveSessionProjection, event: BrowserEvent): LiveSessionProjection {
  return projectLiveSessionEvent(state, event).state;
}
