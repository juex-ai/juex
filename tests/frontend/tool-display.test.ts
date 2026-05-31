import { strict as assert } from "node:assert";
import test from "node:test";

import {
  toolDisplayName,
  toolStatusLabel,
  toolTimeoutLabel,
} from "../../frontend/src/lib/tool-display.ts";

test("toolDisplayName removes transport prefixes from visible titles", () => {
  assert.equal(toolDisplayName("tool-bash"), "bash");
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
