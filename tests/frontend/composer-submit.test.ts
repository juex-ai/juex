import assert from "node:assert/strict";
import test from "node:test";
import {
  composerErrorMessage,
  composerSubmitAction,
  settleSubmittedComposerText,
} from "../../frontend/src/lib/composer-submit.ts";
import type {
  AgentRuntimeStatusSnapshot,
  RuntimeTurnPhase,
} from "../../frontend/src/types.ts";

function status(
  state: AgentRuntimeStatusSnapshot["session"]["state"],
  pendingCount = 0,
  maxPendingInputs = 4,
  phase: RuntimeTurnPhase = "provider_iteration",
  canInterrupt = true,
): AgentRuntimeStatusSnapshot {
  const active = state === "turn_active" || state === "draining_pending";
  return {
    cursor: "1",
    session: {
      id: "session-1",
      state,
      pending_count: pendingCount,
      max_pending_inputs: maxPendingInputs,
      can_accept_input: pendingCount < maxPendingInputs,
    },
    turn: active
      ? {
          id: "turn-1",
          state: "active",
          phase,
          streaming: phase === "provider_iteration",
          can_interrupt: canInterrupt,
          started_at: "",
          updated_at: "",
        }
      : undefined,
    tools: [],
    token_usage: { input_tokens: 0, output_tokens: 0 },
  };
}

test("composerSubmitAction blocks empty idle submissions", () => {
  assert.equal(composerSubmitAction({ status: status("idle"), text: "" }), "empty");
  assert.equal(composerSubmitAction({ status: status("idle"), text: "   " }), "empty");
});

test("composerSubmitAction stops only interruptible active turns with empty input", () => {
  assert.equal(
    composerSubmitAction({ status: status("turn_active"), text: "" }),
    "stop",
  );
  assert.equal(
    composerSubmitAction({
      status: status("turn_active", 0, 4, "compacting"),
      text: "",
    }),
    "stop",
  );
  assert.equal(
    composerSubmitAction({
      status: status("turn_active", 0, 4, "admitted", false),
      text: "",
    }),
    "empty",
  );
  assert.equal(
    composerSubmitAction({
      status: status("turn_active", 0, 4, "compacting", false),
      text: "",
    }),
    "empty",
  );
});

test("composerSubmitAction sends idle input and attachments", () => {
  assert.equal(
    composerSubmitAction({ status: status("idle"), text: "hello" }),
    "send",
  );
  assert.equal(
    composerSubmitAction({
      status: status("idle"),
      text: "",
      attachmentCount: 1,
    }),
    "send",
  );
});

test("composer queues during every active phase including pending drain", () => {
  for (const active of [
    status("turn_active"),
    status("turn_active", 0, 4, "tool_batch"),
    status("turn_active", 0, 4, "compacting"),
    status("draining_pending"),
  ]) {
    assert.equal(
      composerSubmitAction({ status: active, text: "steer next" }),
      "queue",
    );
  }
});

test("composer rejects only when the pending queue is full", () => {
  assert.equal(
    composerSubmitAction({
      status: status("draining_pending", 3, 4),
      text: "one more",
    }),
    "queue",
  );
  assert.equal(
    composerSubmitAction({
      status: status("draining_pending", 4, 4),
      text: "one too many",
    }),
    "queue-full",
  );
});

test("composer uses the live projection until status loads", () => {
  assert.equal(
    composerSubmitAction({
      turnActiveFallback: true,
      text: "",
    }),
    "stop",
  );
  assert.equal(
    composerSubmitAction({
      turnActiveFallback: true,
      text: "queue while loading",
    }),
    "queue",
  );
});

test("composer error prefers runtime failure and falls back to local submit failure", () => {
  const runtimeFailure = status("failed");
  runtimeFailure.last_error = { message: "provider failed" };

  assert.equal(
    composerErrorMessage({
      status: runtimeFailure,
      localError: "proxy unavailable",
    }),
    "provider failed",
  );
  assert.equal(
    composerErrorMessage({
      status: status("idle"),
      localError: "proxy unavailable",
    }),
    "proxy unavailable",
  );
  assert.equal(composerErrorMessage({ status: status("idle") }), undefined);
});

test("successful submit clears only the text that was actually submitted", () => {
  assert.equal(settleSubmittedComposerText("hello", "hello"), "");
  assert.equal(
    settleSubmittedComposerText("hello, and one more thing", "hello"),
    "hello, and one more thing",
  );
  assert.equal(settleSubmittedComposerText("draft ", "draft "), "");
});
