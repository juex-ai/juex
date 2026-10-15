import assert from "node:assert/strict";
import test from "node:test";

import {
  beginLatestRequest,
  invalidateLatestRequest,
} from "../../frontend/src/lib/latest-request.ts";

test("only the newest request generation can apply a response", () => {
  const generation = { current: 0 };
  const firstIsLatest = beginLatestRequest(generation);
  const secondIsLatest = beginLatestRequest(generation);

  assert.equal(firstIsLatest(), false);
  assert.equal(secondIsLatest(), true);
});

test("cleanup invalidates the current request generation", () => {
  const generation = { current: 0 };
  const isLatest = beginLatestRequest(generation);

  invalidateLatestRequest(generation);

  assert.equal(isLatest(), false);
});
