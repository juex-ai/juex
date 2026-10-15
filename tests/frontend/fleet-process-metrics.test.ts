import assert from "node:assert/strict";
import test from "node:test";

import {
  formatProcessCPU,
  formatProcessMemory,
} from "../../frontend/src/lib/fleet-process-metrics.ts";

test("process memory uses compact MB and GB units", () => {
  assert.equal(formatProcessMemory(undefined), "—");
  assert.equal(formatProcessMemory(Number.NaN), "—");
  assert.equal(formatProcessMemory(-1), "—");
  assert.equal(formatProcessMemory(500_000), "0.5 MB");
  assert.equal(formatProcessMemory(100_000_000), "100 MB");
  assert.equal(formatProcessMemory(1_500_000_000), "1.5 GB");
});

test("process CPU preserves single-core and multi-core percentages", () => {
  assert.equal(formatProcessCPU(undefined), "—");
  assert.equal(formatProcessCPU(Number.NaN), "—");
  assert.equal(formatProcessCPU(-1), "—");
  assert.equal(formatProcessCPU(0), "0%");
  assert.equal(formatProcessCPU(100), "100%");
  assert.equal(formatProcessCPU(250.25), "250.3%");
});
