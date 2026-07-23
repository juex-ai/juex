import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const imageBlockSource = readFileSync(
  new URL("../../frontend/src/components/ImageBlock.tsx", import.meta.url),
  "utf8",
);
const transcriptSource = readFileSync(
  new URL(
    "../../frontend/src/components/session/SessionTranscript.tsx",
    import.meta.url,
  ),
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
  assert.match(transcriptSource, /function MessageImageGallery/);
  assert.match(transcriptSource, /role === "user" \? "ml-auto" : "mr-auto"/);
  assert.match(transcriptSource, /grid-cols-2/);
  assert.match(transcriptSource, /media\.push\(candidate\.block\.media \?\? null\)/);
});

test("user attachments lead the message as compact preview thumbnails", () => {
  assert.match(
    transcriptSource,
    /function DefaultMessageGroup[\s\S]*?const userImageMedia =[\s\S]*?\{userImageMedia\.length > 0[\s\S]*?<MessageImageGallery[\s\S]*?group\.units\.map/,
  );
  assert.match(
    transcriptSource,
    /if \(group\.role === "user"\) return null;/,
  );

  assert.match(
    transcriptSource,
    /function MessageImageGallery[\s\S]*?flex w-full flex-wrap justify-end gap-2/,
  );
  assert.match(
    transcriptSource,
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
    transcriptSource,
    /\{result\.media \? \(\s*<ImageBlock media=\{result\.media\} variant="thumbnail" \/>\s*\) : null\}/,
  );
});

test("assistant markdown and direct transcript images use the same thumbnail lightbox", () => {
  assert.match(
    transcriptSource,
    /function MessageImageGallery[\s\S]*?<ImageBlock[\s\S]*?variant="thumbnail"/,
  );
  assert.doesNotMatch(
    transcriptSource,
    /variant=\{role === "user" \? "thumbnail" : "card"\}/,
  );
  assert.match(
    assistantMarkdownSource,
    /<ImageBlock[\s\S]*?variant="thumbnail"[\s\S]*?media=\{mediaByPath\.get\(path\)/,
  );
});
