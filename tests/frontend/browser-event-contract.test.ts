import { readFileSync } from "node:fs";
import { strict as assert } from "node:assert";
import test from "node:test";

import { messagesToGroups } from "../../frontend/src/lib/display-units.ts";
import {
  createLiveSessionProjection,
  projectLiveSessionEvent,
} from "../../frontend/src/lib/live-session-projection.ts";
import {
  BROWSER_EVENT_TYPES,
  type BrowserEvent,
} from "../../frontend/src/types.ts";

test("frontend browser event type list matches backend contract fixture", () => {
  assert.deepEqual(BROWSER_EVENT_TYPES, readJSON("browser-event-types.golden.json"));
});

test("frontend projects backend browser event fixture stream", () => {
  const events = readJSON("browser-events.golden.json") as BrowserEvent[];
  const notesError = events.find((event) => event.type === "notes.errored");
  assert.equal(notesError?.payload.error, "notes read: notes content must be valid UTF-8");
  assert.equal(notesError?.payload.path, "/workspace/.juex/sessions/session-1/notes.md");

  let state = createLiveSessionProjection();
  const effects = [];

  for (const event of events) {
    const result = projectLiveSessionEvent(state, event);
    state = result.state;
    effects.push(...result.effects);
  }

  assert.equal(state.tokenUsage?.input_tokens, 10);
  assert.equal(state.contextUsage?.total_tokens, 40);
  assert.equal(state.compactActive, false);

  const groups = messagesToGroups(state.messages);
  const toolUnit = groups
    .flatMap((group) => group.units)
    .find(
      (unit) =>
        unit.kind === "tool" && unit.use?.tool_use_id === "tool-1",
    );
  assert.equal(toolUnit?.kind, "tool");
  if (toolUnit?.kind === "tool") {
    assert.equal(toolUnit.use?.tool_name, "exec_command");
    assert.equal(toolUnit.result?.content, "hi\n");
  }

  assert.ok(
    state.messages.some(
      (message) =>
        message.kind === "hook_event" &&
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text.includes("hook extract-state"),
    ),
  );
  assert.ok(
    state.messages.some(
      (message) =>
        message.role === "user" &&
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text === "queued follow-up",
    ),
  );
  assert.deepEqual(effects.at(-1), {
    type: "refresh",
    preserveLiveMessages: true,
  });
});

test("pending promotion removes the queued item and starts its live turn", () => {
  const initial = {
    ...createLiveSessionProjection(),
    queuedInput: {
      items: [{ id: "queued-1", input: "after compact", kind: "user" }],
      nextSeq: 1,
    },
  };

  const result = projectLiveSessionEvent(
    initial,
    {
      id: "promoted-1",
      type: "pending_input.promoted",
      ts: "2026-07-19T00:00:00Z",
      turn_id: "turn-2",
      payload: {
        pending_count: 0,
        max_pending_inputs: 16,
      },
    },
  );

  assert.equal(result.state.queuedInput.items.length, 0);
  assert.equal(result.state.turnActive, true);
  assert.ok(
    result.state.messages.some(
      (message) =>
        message.role === "user" &&
        message.turn_id === "turn-2" &&
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text === "after compact",
    ),
  );
  assert.ok(
    result.state.messages.some(
      (message) =>
        message.role === "assistant" &&
        message.turn_id === "turn-2" &&
        message.pending,
    ),
  );
});

test("promoted turn start does not consume an identical next queued item", () => {
  const initial = {
    ...createLiveSessionProjection(),
    queuedInput: {
      items: [
        { id: "queued-1", input: "same input", kind: "user" },
        { id: "queued-2", input: "same input", kind: "user" },
      ],
      nextSeq: 2,
    },
  };

  let state = projectLiveSessionEvent(initial, {
    id: "promoted-1",
    type: "pending_input.promoted",
    ts: "2026-07-19T00:00:00Z",
    turn_id: "turn-2",
    payload: {
      pending_count: 1,
      max_pending_inputs: 16,
    },
  }).state;
  state = projectLiveSessionEvent(state, {
    id: "turn-started-1",
    type: "turn.started",
    ts: "2026-07-19T00:00:01Z",
    turn_id: "turn-2",
    payload: {
      input: "same input",
      kind: "user",
    },
  }).state;

  assert.deepEqual(
    state.queuedInput.items.map((item) => item.id),
    ["queued-2"],
  );
});

test("compact completion projects the post-compaction context usage", () => {
  const initial = {
    ...createLiveSessionProjection(),
    contextUsage: {
      model: "model",
      context_window: 1000,
      input_tokens: 900,
      output_tokens: 0,
      total_tokens: 900,
    },
    compactActive: true,
  };

  const result = projectLiveSessionEvent(
    initial,
    {
      id: "compact-completed-1",
      type: "context.compact.completed",
      ts: "2026-07-19T00:00:00Z",
      turn_id: "compact-1",
      payload: {
        message_id: "summary-1",
        reason: "manual",
        auto: false,
        estimated_tokens: 900,
        tokens_before: 900,
        tokens_after: 40,
        summary_chars: 20,
        summary_model: "model",
        tail_start_message_id: "tail-1",
        context_window: 1000,
        reserve_tokens: 100,
        keep_recent_tokens: 100,
        context_usage: {
          model: "model",
          context_window: 1000,
          input_tokens: 40,
          output_tokens: 0,
          total_tokens: 40,
        },
      },
    },
  );

  assert.equal(result.state.contextUsage?.total_tokens, 40);
  assert.equal(result.state.compactActive, false);
});

function readJSON(name: string): unknown {
  return JSON.parse(
    readFileSync(new URL(`../../internal/web/testdata/${name}`, import.meta.url), "utf8"),
  );
}
