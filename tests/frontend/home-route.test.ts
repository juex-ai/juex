import test from "node:test";
import assert from "node:assert/strict";

import { homeActiveSessionHref } from "../../frontend/src/lib/home-route.ts";

test("homeActiveSessionHref routes to the active primary session", () => {
  assert.equal(
    homeActiveSessionHref("primary/session 1", "/agents/agent%201"),
    "/agents/agent%201/sessions/primary%2Fsession%201",
  );
});

test("homeActiveSessionHref stays on home when no active primary exists", () => {
  assert.equal(homeActiveSessionHref(""), null);
});

test("homeActiveSessionHref handles missing active session ids", () => {
  assert.equal(homeActiveSessionHref(null), null);
  assert.equal(homeActiveSessionHref(undefined), null);
});
