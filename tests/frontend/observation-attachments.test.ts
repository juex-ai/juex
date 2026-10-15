import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const transcriptSource = readFileSync(
  new URL(
    "../../frontend/src/components/thread/ThreadTranscript.tsx",
    import.meta.url,
  ),
  "utf8",
);
const attachmentsSource = readFileSync(
  new URL(
    "../../frontend/src/components/thread/ObservationAttachments.tsx",
    import.meta.url,
  ),
  "utf8",
);

test("observation disclosure names the detail and forwards image blocks", () => {
  assert.match(transcriptSource, /data-observation-title[\s\S]*?Observation/);
  assert.match(
    transcriptSource,
    /const observationMedia =[\s\S]*?unit\.kind === "image"[\s\S]*?<ExternalEventMessage[\s\S]*?media=\{observationMedia\}/,
  );
  assert.match(
    transcriptSource,
    /<ObservationAttachments[\s\S]*?attachments=\{observationAttachments\}[\s\S]*?images=\{media\}/,
  );
});

test("observation attachments use image lightboxes and file preview sheets", () => {
  assert.match(attachmentsSource, /<ImageBlock[\s\S]*?root="artifact"[\s\S]*?variant="thumbnail"/);
  assert.match(attachmentsSource, /displayName=\{name\}/);
  assert.match(attachmentsSource, /remaining\.splice\(metadataIndex, 1\)/);
  assert.match(attachmentsSource, /data-observation-file-attachment/);
  assert.match(
    attachmentsSource,
    /getArtifactContent\(\s*attachment\.artifactPath/,
  );
  assert.match(attachmentsSource, /<Sheet\s+open=\{preview !== null\}/);
  assert.match(attachmentsSource, /Preview truncated at 256 KB\./);
  assert.match(attachmentsSource, /role="alert"/);
  assert.match(
    attachmentsSource,
    /const previewName = attachmentName\([\s\S]*?preview\?\.attachment\.sourcePath[\s\S]*?preview\?\.attachment\.artifactPath/,
  );
  assert.match(
    attachmentsSource,
    /preview\?\.content\?\.kind === "image"[\s\S]*?<ImageBlock[\s\S]*?displayName=\{previewName\}/,
  );
  assert.doesNotMatch(
    attachmentsSource,
    /new Map<string, MediaRef>/,
  );
});
