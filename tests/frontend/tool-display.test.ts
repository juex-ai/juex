import { strict as assert } from "node:assert";
import test from "node:test";

import {
  aggregateToolProcessStatus,
  compactThinkingPreview,
  formatToolBatchTitle,
  formatToolProcessResult,
  formatToolProcessResultText,
  thinkingProcessDisplay,
  thinkingProcessVisibleText,
  toolDisplayName,
  toolProcessStatus,
  toolProcessStatusLabel,
  toolStatusLabel,
  toolTimeoutLabel,
} from "../../frontend/src/lib/tool-display.ts";

test("toolDisplayName removes transport prefixes from visible titles", () => {
  assert.equal(toolDisplayName("tool-shell"), "shell");
  assert.equal(toolDisplayName("tool-tool"), "tool");
  assert.equal(toolDisplayName("dynamic-tool", "edit"), "edit");
  assert.equal(toolDisplayName(null), "tool");
  assert.equal(toolDisplayName("dynamic-tool", null), "tool");
});

test("toolStatusLabel maps tool states to user-facing lifecycle labels", () => {
  assert.equal(toolStatusLabel("input-available"), "running");
  assert.equal(toolStatusLabel("output-available"), "success");
  assert.equal(toolStatusLabel("output-error"), "failed");
});

test("toolTimeoutLabel describes positive running timeouts", () => {
  assert.equal(toolTimeoutLabel(60), "timeout 60s");
  assert.equal(toolTimeoutLabel(undefined), undefined);
  assert.equal(toolTimeoutLabel(0), undefined);
});

test("tool process status maps UI states to compact indicators", () => {
  assert.equal(toolProcessStatus("input-available"), "running");
  assert.equal(toolProcessStatus("output-error"), "failed");
  assert.equal(toolProcessStatus("output-available"), "done");
  assert.equal(toolProcessStatusLabel("running"), "running");
  assert.equal(toolProcessStatusLabel("failed"), "failed");
  assert.equal(toolProcessStatusLabel("done"), "success");
});

test("aggregateToolProcessStatus prioritizes running over failed over done", () => {
  assert.equal(
    aggregateToolProcessStatus(["output-available", "input-available"]),
    "running",
  );
  assert.equal(
    aggregateToolProcessStatus(["output-available", "output-error"]),
    "failed",
  );
  assert.equal(
    aggregateToolProcessStatus(["output-error", "input-available"]),
    "running",
  );
  assert.equal(
    aggregateToolProcessStatus(["output-available", "output-available"]),
    "done",
  );
});

test("formatToolBatchTitle groups names in first-seen order", () => {
  assert.equal(
    formatToolBatchTitle(["memory_write", "memory_write", "update_goal"]),
    "2 memory_write, 1 update_goal",
  );
});

test("compactThinkingPreview truncates after twenty characters", () => {
  assert.equal(
    compactThinkingPreview("inspect current layout carefully"),
    "inspect current layo...",
  );
  assert.equal(compactThinkingPreview("short thought"), "short thought");
});

test("thinkingProcessDisplay shows visible summaries even when replay content is redacted", () => {
  assert.deepEqual(thinkingProcessDisplay("visible summary", true), {
    content: "visible summary",
    title: "Thinking visible summary",
  });
  assert.deepEqual(thinkingProcessDisplay("", true), {
    content: "[redacted]",
    title: "Thinking [redacted]",
  });
});

test("thinkingProcessVisibleText does not expose redacted encrypted content", () => {
  assert.equal(
    thinkingProcessVisibleText({
      text: "visible summary",
      content: "encrypted payload",
      redacted: true,
    }),
    "visible summary",
  );
  assert.equal(
    thinkingProcessVisibleText({
      content: "encrypted payload",
      redacted: true,
    }),
    "",
  );
  assert.equal(
    thinkingProcessVisibleText({
      content: "plain content fallback",
    }),
    "plain content fallback",
  );
  assert.equal(thinkingProcessVisibleText(null), "");
  assert.equal(thinkingProcessVisibleText(undefined), "");
});

test("formatToolProcessResultText caps large process output", () => {
  assert.equal(
    formatToolProcessResultText("alpha\n".repeat(140)).includes(
      "[tool result truncated:",
    ),
    true,
  );
});

test("formatToolProcessResult leaves image-only tool result media to the renderer", () => {
  assert.equal(
    formatToolProcessResult({
      content: "",
      media: {
        artifact_path: ".juex/artifacts/media/s/image.png",
        media_type: "image/png",
        sha256: "abc",
        original_bytes: 12,
        width: 2,
        height: 3,
      },
    }),
    "",
  );
});

test("formatToolProcessResult keeps text separate from tool result media", () => {
  assert.equal(
    formatToolProcessResult({
      content: "chart rendered",
      media: {
        artifact_path: ".juex/artifacts/media/s/chart.png",
        media_type: "image/png",
      },
    }),
    "chart rendered",
  );
});
