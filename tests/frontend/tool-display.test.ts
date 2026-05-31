import { strict as assert } from "node:assert";
import test from "node:test";

import {
  toolStatusLabel,
  toolTimeoutLabel,
} from "../../frontend/src/lib/tool-display.ts";

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
