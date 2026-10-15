import assert from "node:assert/strict";
import test from "node:test";

import { highlightLightCode } from "../../frontend/src/lib/light-code-highlight.ts";

test("highlightLightCode tokenizes JSON values without async Shiki loading", () => {
  const result = highlightLightCode('{"ok":true,"count":2}', "json");

  assert.equal(result.bg, "transparent");
  assert.equal(result.fg, "inherit");
  assert.deepEqual(
    result.tokens[0].map((token) => token.content),
    ["{", '"ok"', ":", "true", ",", '"count"', ":", "2", "}"]
  );
  assert.equal(result.tokens[0][1].color, "var(--juex-forest-700)");
  assert.equal(result.tokens[0][3].color, "var(--juex-info)");
  assert.equal(result.tokens[0][7].color, "var(--juex-gold-900)");
});

test("highlightLightCode leaves non-JSON output as one token per line", () => {
  const result = highlightLightCode("line one\nline two", "log");

  assert.deepEqual(
    result.tokens.map((line) => line.map((token) => token.content)),
    [["line one"], ["line two"]]
  );
});
