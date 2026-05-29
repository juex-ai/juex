import assert from "node:assert/strict";
import test from "node:test";
import { composerSubmitAction } from "../../frontend/src/lib/composer-submit.ts";

test("composerSubmitAction blocks empty idle submissions", () => {
  assert.equal(composerSubmitAction({ busy: false, text: "" }), "empty");
  assert.equal(composerSubmitAction({ busy: false, text: "   " }), "empty");
});

test("composerSubmitAction stops the active turn when empty and busy", () => {
  assert.equal(composerSubmitAction({ busy: true, text: "" }), "stop");
});

test("composerSubmitAction sends non-empty idle input", () => {
  assert.equal(composerSubmitAction({ busy: false, text: "hello" }), "send");
});

test("composerSubmitAction queues non-empty input while busy", () => {
  assert.equal(composerSubmitAction({ busy: true, text: "steer next" }), "queue");
});
