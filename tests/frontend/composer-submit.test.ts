import assert from "node:assert/strict";
import test from "node:test";
import { composerSubmitAction } from "../../frontend/src/lib/composer-submit.ts";

test("composerSubmitAction blocks empty idle submissions", () => {
  assert.equal(
    composerSubmitAction({ turnActive: false, compactActive: false, text: "" }),
    "empty",
  );
  assert.equal(
    composerSubmitAction({
      turnActive: false,
      compactActive: false,
      text: "   ",
    }),
    "empty",
  );
});

test("composerSubmitAction stops the active turn when empty and busy", () => {
  assert.equal(
    composerSubmitAction({ turnActive: true, compactActive: false, text: "" }),
    "stop",
  );
});

test("composerSubmitAction blocks empty compact submissions", () => {
  assert.equal(
    composerSubmitAction({ turnActive: false, compactActive: true, text: "" }),
    "compacting",
  );
});

test("composerSubmitAction sends non-empty idle input", () => {
  assert.equal(
    composerSubmitAction({
      turnActive: false,
      compactActive: false,
      text: "hello",
    }),
    "send",
  );
});

test("composerSubmitAction queues non-empty input while busy", () => {
  assert.equal(
    composerSubmitAction({
      turnActive: true,
      compactActive: false,
      text: "steer next",
    }),
    "queue",
  );
  assert.equal(
    composerSubmitAction({
      turnActive: false,
      compactActive: true,
      text: "steer next",
    }),
    "queue",
  );
});
