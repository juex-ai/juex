import assert from "node:assert/strict";
import test from "node:test";

import { loadingStateLabel } from "../../frontend/src/lib/loading-state.ts";

test("loadingStateLabel keeps loading copy stable and concise", () => {
  assert.equal(
    loadingStateLabel("Loading conversation"),
    "Loading conversation",
  );
  assert.equal(loadingStateLabel("  Loading runtime  "), "Loading runtime");
  assert.equal(loadingStateLabel(""), "Loading");
});
