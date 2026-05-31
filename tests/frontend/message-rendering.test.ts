import assert from "node:assert/strict";
import test from "node:test";
import { messageResponseClassName } from "../../frontend/src/lib/message-rendering.ts";

test("messageResponseClassName preserves explicit paragraph newlines", () => {
  assert.match(messageResponseClassName(), /\[&_p\]:whitespace-pre-wrap/);
});
