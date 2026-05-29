import assert from "node:assert/strict";
import test from "node:test";
import { formatToolPayload } from "../../frontend/src/lib/tool-payload.ts";

test("formatToolPayload returns a fallback for missing tool payloads", () => {
  assert.equal(formatToolPayload(undefined), "{}");
  assert.equal(formatToolPayload(null), "{}");
  assert.equal(formatToolPayload(undefined, "null"), "null");
  assert.equal(formatToolPayload(null, "null"), "null");
});

test("formatToolPayload serializes JSON payloads", () => {
  assert.equal(
    formatToolPayload({ path: "README.md" }),
    '{\n  "path": "README.md"\n}',
  );
});

test("formatToolPayload keeps rendering when payloads cannot be serialized", () => {
  const value: Record<string, unknown> = {};
  value.self = value;

  assert.equal(formatToolPayload(value), "[object Object]");

  const noPrototypeValue: Record<string, unknown> = Object.create(null);
  noPrototypeValue.self = noPrototypeValue;

  assert.equal(formatToolPayload(noPrototypeValue), "[unserializable]");
});
