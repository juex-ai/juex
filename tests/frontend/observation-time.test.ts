import assert from "node:assert/strict";
import test from "node:test";

import {
  formatObservationTimestamp,
  formatObservationWindow,
} from "../../frontend/src/lib/observation-time.ts";

test("formatObservationTimestamp renders local time at second precision", () => {
  const local = new Date(2026, 6, 7, 18, 12, 1, 987);

  assert.equal(formatObservationTimestamp(local.getTime()), "20260707 18:12:01");
});

test("formatObservationWindow renders a local second-precision range", () => {
  const start = new Date(2026, 6, 7, 18, 12, 1);
  const end = new Date(2026, 6, 7, 18, 12, 11);

  assert.equal(
    formatObservationWindow(start.getTime(), end.getTime()),
    "20260707 18:12:01 - 20260707 18:12:11",
  );
});

test("formatObservationTimestamp keeps invalid values visible", () => {
  assert.equal(formatObservationTimestamp(undefined), "-");
  assert.equal(formatObservationTimestamp(0), "-");
  assert.equal(formatObservationTimestamp("0"), "-");
  assert.equal(formatObservationTimestamp("not-a-date"), "not-a-date");
});
