import test from "node:test";
import assert from "node:assert/strict";

import {
  formatRuntimeTokenCount,
  runtimeGoalBadgeLabel,
  runtimeHookCommandLabel,
  runtimeHooksSummaryLabel,
  runtimeWorkingStateBadgeLabel,
  workingStatePresenceLabel,
  workingStateRecordCount,
  workingStateSectionCounts,
} from "../../frontend/src/lib/runtime-display.ts";

test("formatRuntimeTokenCount keeps sub-thousand counts exact", () => {
  assert.equal(formatRuntimeTokenCount(999), "999");
});

test("formatRuntimeTokenCount formats large counts without 1000k", () => {
  assert.equal(formatRuntimeTokenCount(999_950), "1m");
  assert.equal(formatRuntimeTokenCount(1_250_000), "1.3m");
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
  assert.equal(runtimeGoalBadgeLabel({ status: "continue" }), "goal continue");
});

test("workingStatePresenceLabel describes active sidecar state", () => {
  assert.equal(workingStatePresenceLabel(undefined), "no active session");
  assert.equal(
    workingStatePresenceLabel({ disabled: true, present: false, state: { version: 1 } }),
    "disabled",
  );
  assert.equal(
    workingStatePresenceLabel({ present: false, state: { version: 1 } }),
    "empty",
  );
  assert.equal(
    workingStatePresenceLabel({ present: true, state: { version: 1 } }),
    "present",
  );
});

test("workingStateSectionCounts summarizes sidecar records", () => {
  const counts = workingStateSectionCounts({
    present: true,
    state: {
      version: 1,
      goal: { text: "ship it" },
      hard_constraints: [{ text: "test first" }],
      open_issues: [{ text: "missing e2e" }, { text: "missing docs" }],
    },
  });
  assert.equal(counts.find((item) => item.key === "goal")?.count, 1);
  assert.equal(counts.find((item) => item.key === "hard_constraints")?.count, 1);
  assert.equal(counts.find((item) => item.key === "open_issues")?.count, 2);
  assert.equal(counts.find((item) => item.key === "stale_checks")?.count, 0);
  assert.equal(workingStateRecordCount({ present: true, state: countsState() }), 4);
  assert.equal(
    runtimeWorkingStateBadgeLabel({ present: true, state: countsState() }),
    "state 4",
  );
});

function countsState() {
  return {
    version: 1,
    goal: { text: "ship it" },
    hard_constraints: [{ text: "test first" }],
    open_issues: [{ text: "missing e2e" }, { text: "missing docs" }],
  };
}
