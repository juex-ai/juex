import test from "node:test";
import assert from "node:assert/strict";

import { isHistoryPath } from "../../frontend/src/lib/route-state.ts";

test("isHistoryPath matches only the history list page", () => {
  assert.equal(isHistoryPath("/history"), true);
  assert.equal(isHistoryPath("/history/sessions/abc"), false);
  assert.equal(isHistoryPath("/history-settings"), false);
  assert.equal(isHistoryPath("/sessions/history"), false);
});
