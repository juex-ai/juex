import assert from "node:assert/strict";
import test from "node:test";
import type { NotesSnapshot } from "../../frontend/src/module-schema.ts";
import { runtimeGoalBadgeLabel, runtimeGoalIsActive, runtimeGoalContinuationLabel } from "../../frontend/src/modules/goal/display.ts";
import { notesCheckboxProgress, notesBadgeLabel } from "../../frontend/src/modules/notes/display.ts";

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

test("notes labels distinguish an enabled empty module from progress", () => {
  assert.equal(notesBadgeLabel(), "notes empty");
  assert.equal(notesBadgeLabel({ content: "- [x] done\n- [ ] next" }), "notes 1/2");
  assert.equal(notesBadgeLabel({ content: "working" }), "notes active");
});
