import test from "node:test";
import assert from "node:assert/strict";

import { homeActiveSessionHref } from "../../frontend/src/lib/home-route.ts";

test("homeActiveSessionHref routes to the active primary session", () => {
  assert.equal(
    homeActiveSessionHref([
      { id: "side", kind: "side", active: false },
      { id: "primary/session 1", kind: "primary", active: true },
    ]),
    "/sessions/primary%2Fsession%201",
  );
});

test("homeActiveSessionHref stays on home when no active primary exists", () => {
  assert.equal(
    homeActiveSessionHref([
      { id: "side", kind: "side", active: false },
      { id: "primary", kind: "primary", active: false },
    ]),
    null,
  );
  assert.equal(homeActiveSessionHref([]), null);
});

test("homeActiveSessionHref handles missing session lists", () => {
  assert.equal(homeActiveSessionHref(null), null);
  assert.equal(homeActiveSessionHref(undefined), null);
});
