import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sessionSource = readFileSync(
  new URL("../../frontend/src/pages/Session.tsx", import.meta.url),
  "utf8",
);
const queuedSource = readFileSync(
  new URL("../../frontend/src/components/QueuedInputStack.tsx", import.meta.url),
  "utf8",
);

test("composer groups utility actions before matching status controls", () => {
  const actions = sessionSource.indexOf('aria-label="Composer actions"');
  const separator = sessionSource.indexOf('orientation="vertical"');
  const status = sessionSource.indexOf('aria-label="Session status"');
  assert.ok(actions >= 0 && separator > actions && status > separator);
  assert.match(
    sessionSource,
    /aria-label="Composer actions"\s+role="group"/,
  );
  assert.match(
    sessionSource,
    /aria-label="Session status"\s+role="group"/,
  );
  assert.match(sessionSource, /COMPOSER_STATUS_CONTROL_CLASS/);
  assert.match(sessionSource, /<PopoverTrigger asChild>/);
  assert.doesNotMatch(
    sessionSource,
    /ContextUsageLabel[\s\S]{0,600}<TooltipTrigger/,
  );
});

test("composer goal chip names the disclosed goal and notes content", () => {
  assert.match(
    sessionSource,
    /aria-label=\{`Open goal and notes: \$\{label\}`\}/,
  );
});

test("composer stages image previews above draft text at the top-left", () => {
  const composer = sessionSource.match(
    /<PromptInput\s[\s\S]*?<\/PromptInput>/,
  )?.[0];
  assert.ok(composer);
  const attachmentStrip = composer.indexOf("<ComposerAttachmentStrip");
  const textarea = composer.indexOf("<PromptInputTextarea");
  assert.ok(
    attachmentStrip >= 0 && textarea > attachmentStrip,
    "attachment strip should render before the textarea",
  );

  const strip = sessionSource.match(
    /function ComposerAttachmentStrip[\s\S]*?\n}\n\nfunction ComposerSubmitButton/,
  )?.[0];
  assert.ok(strip);
  const stripClassName = strip.match(
    /aria-label="Attached images"\s+className="([^"]+)"/,
  )?.[1];
  assert.ok(stripClassName);
  const stripClasses = new Set(stripClassName.split(/\s+/));
  for (const expectedClass of [
    "flex",
    "w-full",
    "flex-wrap",
    "items-start",
    "justify-start",
    "gap-2",
    "px-2.5",
    "pt-2",
  ]) {
    assert.ok(
      stripClasses.has(expectedClass),
      `attachment strip should include ${expectedClass}`,
    );
  }
  assert.doesNotMatch(strip, /border-t|min-h-20/);
  assert.match(strip, /aria-label="Attached images"/);
  for (const expectedClass of ["size-20", "shrink-0"]) {
    assert.match(strip, new RegExp(`className="[^"]*\\b${expectedClass}\\b`));
  }
  for (const expectedClass of [
    "rounded-full",
    "bg-foreground",
    "text-background",
  ]) {
    assert.match(strip, new RegExp(`\\b${expectedClass}\\b`));
  }
});

test("composer feedback is announced and queued inputs stay bounded", () => {
  assert.match(sessionSource, /role=\{tone === "error" \? "alert" : "status"\}/);
  assert.match(sessionSource, /aria-live=/);
  assert.match(queuedSource, /max-h-/);
  assert.match(queuedSource, /overflow-y-auto/);
  assert.match(queuedSource, /Queued.*items\.length/s);
  assert.match(queuedSource, /text-foreground group-open:hidden/);
});

test("deferred submit keeps follow-up text and attachment counts authoritative", () => {
  assert.match(sessionSource, /settleSubmittedComposerText\(current, submittedText\)/);
  assert.doesNotMatch(sessionSource, /setAttachmentCount\(0\)/);
});
