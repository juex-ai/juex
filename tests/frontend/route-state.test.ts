import test from "node:test";
import assert from "node:assert/strict";

import { isHistoryPath } from "../../frontend/src/lib/route-state.ts";

test("isHistoryPath matches only the history route tree", () => {
  assert.equal(isHistoryPath("/history"), true);
  assert.equal(isHistoryPath("/history/sessions/abc"), true);
  assert.equal(isHistoryPath("/history-settings"), false);
  assert.equal(isHistoryPath("/sessions/history"), false);
});
