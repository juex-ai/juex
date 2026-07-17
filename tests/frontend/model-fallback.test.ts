import assert from "node:assert/strict";
import test from "node:test";

import { formatModelFallbackNotice } from "../../frontend/src/lib/model-fallback.ts";
import { messageGroupCanCopy } from "../../frontend/src/lib/message-copy.ts";

test("model fallback notice strips provider-only reminder wrapper", () => {
  const display = formatModelFallbackNotice(
    "<system-reminder>The previous model failed. Work moved to backup:model.</system-reminder>",
  );
  assert.equal(display.title, "Model switched");
  assert.equal(
    display.content,
    "The previous model failed. Work moved to backup:model.",
  );
});

test("model recovery notice gets recovery title", () => {
  const display = formatModelFallbackNotice(
    "<system-reminder>A higher-priority model is healthy again and was switched back.</system-reminder>",
  );
  assert.equal(display.title, "Model recovered");
});

test("model fallback process notices are not copied as user chat", () => {
  assert.equal(
    messageGroupCanCopy({
      key: "fallback",
      role: "user",
      kind: "model_fallback",
      pending: false,
      units: [{ kind: "text", block: { type: "text", text: "switched" } }],
    }),
    false,
  );
});
