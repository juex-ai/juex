import assert from "node:assert/strict";
import test from "node:test";
import {
  isCompactCommandInput,
  isLocalCompactMessage,
  LOCAL_COMPACT_PENDING_ID,
  LOCAL_COMPACT_PENDING_KIND,
  PENDING_COMPACT_LABEL,
} from "../../frontend/src/lib/compact-ui.ts";

test("isCompactCommandInput matches only bare compact commands", () => {
  assert.equal(isCompactCommandInput("/compact"), true);
  assert.equal(isCompactCommandInput("  /compact  "), true);
  assert.equal(isCompactCommandInput("/compact now"), false);
  assert.equal(isCompactCommandInput("/status"), false);
});

test("pending compact label matches the in-progress divider copy", () => {
  assert.equal(PENDING_COMPACT_LABEL, "Context compacting...");
});

test("isLocalCompactMessage identifies optimistic compact UI messages", () => {
  assert.equal(isLocalCompactMessage({ id: LOCAL_COMPACT_PENDING_ID }), true);
  assert.equal(isLocalCompactMessage({ kind: LOCAL_COMPACT_PENDING_KIND }), true);
  assert.equal(isLocalCompactMessage({ id: "persisted", kind: "compact" }), false);
});
