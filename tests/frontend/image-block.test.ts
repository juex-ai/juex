import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const imageBlockSource = readFileSync(
  new URL("../../frontend/src/components/ImageBlock.tsx", import.meta.url),
  "utf8",
);
const sessionSource = readFileSync(
  new URL("../../frontend/src/pages/Session.tsx", import.meta.url),
  "utf8",
);

test("image lightbox uses the modal dialog primitive", () => {
  assert.match(imageBlockSource, /<Dialog[\s>]/);
  assert.match(imageBlockSource, /<DialogTrigger asChild>/);
  assert.match(imageBlockSource, /<DialogContent/);
  assert.match(imageBlockSource, /<DialogClose asChild>/);
  assert.match(imageBlockSource, /aria-describedby/);
  assert.match(imageBlockSource, /focus-visible:ring-2/);
  assert.match(imageBlockSource, /onError=\{\(\) => setFailed\(true\)\}/);
  assert.match(imageBlockSource, /const \[previewFailed, setPreviewFailed\]/);
  assert.match(imageBlockSource, /onError=\{\(\) => setPreviewFailed\(true\)\}/);
  assert.match(imageBlockSource, /Failed to load full-size image/);
  assert.match(imageBlockSource, /if \(!Number\.isFinite\(bytes\) \|\| bytes <= 0\)/);
});

test("message images follow role alignment and consecutive images form a gallery", () => {
  assert.match(imageBlockSource, /className\?: string/);
  assert.match(imageBlockSource, /className=\{cn\(/);
  assert.match(sessionSource, /function MessageImageGallery/);
  assert.match(sessionSource, /role === "user" \? "ml-auto" : "mr-auto"/);
  assert.match(sessionSource, /grid-cols-2/);
  assert.match(sessionSource, /media\.push\(candidate\.block\.media \?\? null\)/);
});
