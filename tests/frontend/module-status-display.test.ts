import assert from "node:assert/strict";
import test from "node:test";
import type { NotesSnapshot, TasksSnapshot } from "../../frontend/src/module-schema.ts";
import { runtimeTasksBadgeLabel, runtimeTasksIsActive } from "../../frontend/src/modules/tasks/display.ts";
import { notesCheckboxProgress, notesBadgeLabel } from "../../frontend/src/modules/notes/display.ts";

const taskState = (...statuses: string[]): TasksSnapshot => ({ tasks: statuses.map((status, i) => ({ id: String(i), title: "work", description: "work", acceptance: "", status, status_reason: "", priority: "p1", continuation_count: 0, updated_at: "" })) });

test("task badge reports completion across the whole list", () => {
  assert.equal(runtimeTasksBadgeLabel(), "tasks empty");
  assert.equal(runtimeTasksBadgeLabel(taskState("doing", "done", "pending")), "tasks 1/3");
});

test("only todo and doing task lists are active", () => {
  assert.equal(runtimeTasksIsActive(), false);
  assert.equal(runtimeTasksIsActive(taskState("pending", "failed", "done")), false);
  assert.equal(runtimeTasksIsActive(taskState("todo", "pending")), true);
  assert.equal(runtimeTasksIsActive(taskState("doing")), true);
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
