import assert from "node:assert/strict";
import test from "node:test";
import { formatToolResultText } from "../../frontend/src/lib/tool-result-output.ts";

test("formatToolResultText preserves result newlines", () => {
  const formatted = formatToolResultText("first line\nsecond line\n\nfourth line", {
    maxChars: 1000,
    maxLines: 20,
  });

  assert.equal(formatted.text, "first line\nsecond line\n\nfourth line");
  assert.equal(formatted.truncated, false);
  assert.equal(formatted.omittedChars, 0);
  assert.equal(formatted.omittedLines, 0);
});

test("formatToolResultText caps lines without flattening visible output", () => {
  const formatted = formatToolResultText("alpha\nbeta\ngamma", {
    maxChars: 1000,
    maxLines: 2,
  });

  assert.equal(
    formatted.text,
    "alpha\nbeta\n[tool result truncated: 1 more line]"
  );
  assert.equal(formatted.truncated, true);
  assert.equal(formatted.omittedLines, 1);
});

test("formatToolResultText caps total characters", () => {
  const formatted = formatToolResultText("abcdef", {
    maxChars: 3,
    maxLines: 20,
  });

  assert.equal(
    formatted.text,
    "abc\n[tool result truncated: 3 more characters]"
  );
  assert.equal(formatted.truncated, true);
  assert.equal(formatted.omittedChars, 3);
});
