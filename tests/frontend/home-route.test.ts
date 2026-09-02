import test from "node:test";
import assert from "node:assert/strict";

import { homeActiveThreadHref } from "../../frontend/src/lib/home-route.ts";

test("homeActiveThreadHref routes to the active primary thread", () => {
  assert.equal(
    homeActiveThreadHref("primary/thread 1", "/agents/agent%201"),
    "/agents/agent%201/threads/primary%2Fthread%201",
  );
});

test("homeActiveThreadHref stays on home when no active primary exists", () => {
  assert.equal(homeActiveThreadHref(""), null);
});

test("homeActiveThreadHref handles missing active thread ids", () => {
  assert.equal(homeActiveThreadHref(null), null);
  assert.equal(homeActiveThreadHref(undefined), null);
});
