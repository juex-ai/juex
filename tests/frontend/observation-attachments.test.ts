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
  assert.match(attachmentsSource, /data-observation-file-attachment/);
  assert.match(
    attachmentsSource,
    /getArtifactContent\(\s*attachment\.artifactPath/,
  );
  assert.match(attachmentsSource, /<Sheet\s+open=\{preview !== null\}/);
  assert.match(attachmentsSource, /Preview truncated at 256 KB\./);
  assert.match(attachmentsSource, /role="alert"/);
  assert.doesNotMatch(
    attachmentsSource,
    /imageByPath\.set\(attachment\.artifactPath/,
  );
});
