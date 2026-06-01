import test from "node:test";
import assert from "node:assert/strict";

import { formatRuntimeTokenCount } from "../../frontend/src/lib/runtime-display.ts";

test("formatRuntimeTokenCount keeps sub-thousand counts exact", () => {
  assert.equal(formatRuntimeTokenCount(999), "999");
});

test("formatRuntimeTokenCount formats large counts without 1000k", () => {
  assert.equal(formatRuntimeTokenCount(999_950), "1m");
  assert.equal(formatRuntimeTokenCount(1_250_000), "1.3m");
});
