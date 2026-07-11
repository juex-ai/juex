import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const imageBlockSource = readFileSync(
  new URL("../../frontend/src/components/ImageBlock.tsx", import.meta.url),
  "utf8",
);

test("image lightbox closes on Escape and removes its listener", () => {
  assert.match(imageBlockSource, /event\.key === "Escape"/);
  assert.match(
    imageBlockSource,
    /window\.addEventListener\("keydown", handleKeyDown\)/,
  );
  assert.match(
    imageBlockSource,
    /window\.removeEventListener\("keydown", handleKeyDown\)/,
  );
});
