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
const assistantMarkdownSource = readFileSync(
  new URL("../../frontend/src/components/AssistantMarkdown.tsx", import.meta.url),
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

test("user attachments lead the message as compact preview thumbnails", () => {
  assert.match(
    sessionSource,
    /function MessageGroupView[\s\S]*?const userImageMedia =[\s\S]*?\{userImageMedia\.length > 0[\s\S]*?<MessageImageGallery[\s\S]*?group\.units\.map/,
  );
  assert.match(
    sessionSource,
    /if \(group\.role === "user"\) return null;/,
  );

  assert.match(
    sessionSource,
    /function MessageImageGallery[\s\S]*?flex w-full flex-wrap justify-end gap-2/,
  );
  assert.match(
    sessionSource,
    /function MessageImageGallery[\s\S]*?variant="thumbnail"/,
  );

  assert.match(
    imageBlockSource,
    /variant\?: "card" \| "thumbnail"/,
  );
  assert.match(imageBlockSource, /variant = "card"/);
  assert.match(imageBlockSource, /const isThumbnail = variant === "thumbnail"/);

  const failedFallback = imageBlockSource.match(
    /if \(!path \|\| failed\) \{[\s\S]*?\n  \}\n\n  const src/,
  )?.[0];
  assert.ok(failedFallback);
  assert.match(failedFallback, /isThumbnail[\s\S]*?\bsize-20\b/);

  const figure = imageBlockSource.match(/<figure[\s\S]*?<\/figure>/)?.[0];
  assert.ok(figure);
  assert.match(figure, /isThumbnail[\s\S]*?\bsize-20\b/);
  assert.match(figure, /isThumbnail \? "size-full" : "w-full text-left"/);
  assert.match(figure, /isThumbnail[\s\S]*?"size-full object-cover"/);
  assert.match(imageBlockSource, /aria-label=\{isThumbnail \? `Preview \$\{name\}`/);
  assert.match(
    imageBlockSource,
    /aria-describedby=\{isThumbnail \? undefined : captionID\}/,
  );
  assert.match(figure, /\{!isThumbnail \? \(\s*<figcaption/);
  assert.match(figure, /aria-label=\{`Download \$\{name\}`\}/);
  assert.match(
    sessionSource,
    /\{result\.media \? \(\s*<ImageBlock media=\{result\.media\} variant="thumbnail" \/>\s*\) : null\}/,
  );
});

test("assistant markdown and direct transcript images use the same thumbnail lightbox", () => {
  assert.match(
    sessionSource,
    /function MessageImageGallery[\s\S]*?<ImageBlock[\s\S]*?variant="thumbnail"/,
  );
  assert.doesNotMatch(
    sessionSource,
    /variant=\{role === "user" \? "thumbnail" : "card"\}/,
  );
  assert.match(
    assistantMarkdownSource,
    /<ImageBlock[\s\S]*?variant="thumbnail"[\s\S]*?media=\{mediaByPath\.get\(path\)/,
  );
});
