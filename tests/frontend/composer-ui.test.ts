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
