import test from "node:test";
import assert from "node:assert/strict";

import type { NotesSnapshot } from "../../frontend/src/types.ts";

import {
  formatRuntimeTokenCount,
  notesCheckboxProgress,
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeGoalBadgeLabel,
  runtimeGoalContinuationLabel,
  runtimeGoalIsActive,
  runtimeHookCommandLabel,
  runtimeHooksSummaryLabel,
  runtimeSessionStateBadgeLabel,
  runtimeSessionStateIsActive,
  runtimeTokenUsageDetailLabel,
} from "../../frontend/src/lib/runtime-display.ts";

test("formatRuntimeTokenCount keeps sub-thousand counts exact", () => {
  assert.equal(formatRuntimeTokenCount(999), "999");
});

test("formatRuntimeTokenCount formats large counts without 1000k", () => {
  assert.equal(formatRuntimeTokenCount(999_950), "1m");
  assert.equal(formatRuntimeTokenCount(1_250_000), "1.3m");
});

test("runtimeContextPercentLabel summarizes context window usage", () => {
  assert.equal(runtimeContextPercentLabel(undefined), "-");
  assert.equal(
    runtimeContextPercentLabel({
      context_window: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 42,
    }),
    "-",
  );
  assert.equal(
    runtimeContextPercentLabel({
      context_window: 10_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 5_950,
    }),
    "~59.5%",
  );
  assert.equal(
    runtimeContextPercentLabel({
      context_window: 10_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
    }),
    "~0%",
  );
});

test("runtime context tooltip labels separate model, window, and total tokens", () => {
  assert.equal(
    runtimeContextModelLabel({
      model: " clip-local:gemini-3.1-pro-low ",
      context_window: 256_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 156_800,
    }),
    "clip-local:gemini-3.1-pro-low",
  );
  assert.equal(
    runtimeContextWindowDetailLabel({
      model: "clip-local:gemini-3.1-pro-low",
      context_window: 256_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 156_800,
    }),
    "context window: ~156.8k/256k tokens (~61.3%)",
  );
  assert.equal(
    runtimeTokenUsageDetailLabel({
      input_tokens: 22_900_000,
      output_tokens: 39_300,
    }),
    "total tokens: 22.9m in / 39.3k out",
  );
  assert.equal(runtimeContextModelLabel(undefined), "unknown");
  assert.equal(
    runtimeContextWindowDetailLabel(undefined),
    "context window: 0/0 tokens (0%)",
  );
});

test("runtimeHooksSummaryLabel pluralizes configured hooks", () => {
  assert.equal(runtimeHooksSummaryLabel({ configured: 0, commands: [] }), "0 hooks");
  assert.equal(
    runtimeHooksSummaryLabel({
      configured: 1,
      commands: [
        {
          name: "guard",
          events: ["PreToolUse"],
          command: ["python3", "guard.py"],
          timeout_seconds: 10,
          max_output_bytes: 65536,
        },
      ],
    }),
    "1 hook",
  );
});

test("runtimeHookCommandLabel joins command argv", () => {
  assert.equal(runtimeHookCommandLabel(["python3", "guard.py"]), "python3 guard.py");
  assert.equal(runtimeHookCommandLabel([]), "-");
});

test("runtimeGoalBadgeLabel summarizes goal status", () => {
  assert.equal(runtimeGoalBadgeLabel(undefined), "goal none");
  assert.equal(runtimeGoalBadgeLabel({ status: "in_progress" }), "goal in_progress");
});

test("runtimeGoalIsActive only highlights real goal statuses", () => {
  assert.equal(runtimeGoalIsActive(undefined), false);
  assert.equal(runtimeGoalIsActive({ status: "" }), false);
  assert.equal(runtimeGoalIsActive({ status: "none" }), false);
  assert.equal(runtimeGoalIsActive({ status: "in_progress" }), true);
});

test("runtimeGoalContinuationLabel reads simplified continuation count", () => {
  assert.equal(runtimeGoalContinuationLabel(undefined), "-");
  assert.equal(runtimeGoalContinuationLabel({ status: "in_progress" }), "0");
  assert.equal(runtimeGoalContinuationLabel({ status: "in_progress", continuation_count: 2 }), "2");
});

test("notesCheckboxProgress counts Markdown task items", () => {
  assert.deepEqual(notesCheckboxProgress(undefined), {
    completed: 0,
    total: 0,
    percent: 0,
  });
  const progress = notesCheckboxProgress({
    content: "- [x] inspect\n  - [X] implement\n- [ ] verify\nplain text",
  });
  assert.equal(progress.completed, 2);
  assert.equal(progress.total, 3);
  assert.ok(Math.abs(progress.percent - 200 / 3) < 1e-10);
  assert.deepEqual(notesCheckboxProgress({} as NotesSnapshot), {
    completed: 0,
    total: 0,
    percent: 0,
  });
});

test("runtimeSessionStateBadgeLabel keeps footer label compact", () => {
  assert.equal(runtimeSessionStateBadgeLabel(), "state");
});

test("runtimeSessionStateIsActive merges goal and notes presence", () => {
  assert.equal(runtimeSessionStateIsActive(undefined, undefined), false);
  assert.equal(runtimeSessionStateIsActive({ status: "in_progress" }, undefined), true);
  assert.equal(
    runtimeSessionStateIsActive(undefined, { content: "- [ ] verify" }),
    true,
  );
  assert.equal(runtimeSessionStateIsActive(undefined, { content: "  " }), false);
  assert.equal(runtimeSessionStateIsActive(undefined, {} as NotesSnapshot), false);
  assert.equal(
    runtimeSessionStateIsActive(undefined, { content: null } as unknown as NotesSnapshot),
    false,
  );
});
