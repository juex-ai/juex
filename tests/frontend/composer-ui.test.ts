import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sessionSource = readFileSync(
  new URL("../../frontend/src/pages/Session.tsx", import.meta.url),
  "utf8",
);
const composerSource = readFileSync(
  new URL(
    "../../frontend/src/components/session/SessionComposer.tsx",
    import.meta.url,
  ),
  "utf8",
);
const statusSource = readFileSync(
  new URL(
    "../../frontend/src/components/session/SessionStatusPanel.tsx",
    import.meta.url,
  ),
  "utf8",
);
const controllerSource = readFileSync(
  new URL(
    "../../frontend/src/lib/session-read-controller.ts",
    import.meta.url,
  ),
  "utf8",
);
const queuedSource = readFileSync(
  new URL("../../frontend/src/components/QueuedInputStack.tsx", import.meta.url),
  "utf8",
);
const promptInputSource = readFileSync(
  new URL(
    "../../frontend/src/components/ai-elements/prompt-input.tsx",
    import.meta.url,
  ),
  "utf8",
);

test("composer groups utility actions before matching status controls", () => {
  const actions = composerSource.indexOf('aria-label="Composer actions"');
  const separator = composerSource.indexOf('orientation="vertical"');
  const status = composerSource.indexOf('aria-label="Session status"');
  assert.ok(actions >= 0 && separator > actions && status > separator);
  assert.match(
    composerSource,
    /aria-label="Composer actions"\s+role="group"/,
  );
  assert.match(
    composerSource,
    /aria-label="Session status"\s+role="group"/,
  );
  assert.match(statusSource, /STATUS_CONTROL_CLASS/);
  assert.match(statusSource, /<PopoverTrigger asChild>/);
  assert.doesNotMatch(
    statusSource,
    /ContextUsageLabel[\s\S]{0,600}<TooltipTrigger/,
  );
});

test("composer goal chip names the disclosed goal and notes content", () => {
  assert.match(
    statusSource,
    /aria-label=\{`Open goal and notes: \$\{label\}`\}/,
  );
});

test("composer stages image previews above draft text at the top-left", () => {
  const composer = composerSource.match(
    /<PromptInput\s[\s\S]*?<\/PromptInput>/,
  )?.[0];
  assert.ok(composer);
  const attachmentStrip = composer.indexOf("<ComposerAttachmentStrip");
  const textarea = composer.indexOf("<PromptInputTextarea");
  assert.ok(
    attachmentStrip >= 0 && textarea > attachmentStrip,
    "attachment strip should render before the textarea",
  );

  const strip = composerSource.match(
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
    "max-h-[min(10.5rem,24dvh)]",
    "w-full",
    "flex-wrap",
    "items-start",
    "justify-start",
    "gap-2",
    "overflow-y-auto",
    "overscroll-contain",
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
  assert.match(composerSource, /role=\{tone === "error" \? "alert" : "status"\}/);
  assert.match(composerSource, /aria-live=/);
  assert.match(queuedSource, /max-h-/);
  assert.match(queuedSource, /overflow-y-auto/);
  assert.match(queuedSource, /Queued.*items\.length/s);
  assert.match(queuedSource, /text-foreground group-open:hidden/);
});

test("blocked keyboard submissions preserve the composer draft", () => {
  assert.match(
    composerSource,
    /submitAction === "loading"[\s\S]*?throw new Error\("Loading session status"\)/,
    "loading status must reject form submission so PromptInput does not clear the draft",
  );
  assert.match(
    composerSource,
    /submitAction === "queue-full"[\s\S]*?throw new Error\(QUEUE_FULL_SUBMIT_HINT\)/,
    "a full queue must reject form submission so PromptInput does not clear the draft",
  );
});

test("controller owns status application and close-before-clear cleanup", () => {
  assert.match(
    controllerSource,
    /if \(!subscribed \|\| !isLatestSessionRoute\(route, sessionID\)\) return;[\s\S]*status\.apply\(sessionID, event\.status\)/,
    "the controller must reject disposed or stale frames before applying status",
  );
  assert.match(
    controllerSource,
    /generation !== refreshGeneration \|\|[\s\S]*revision !== statusRevision/,
    "a stale status calibration must not replace an event snapshot",
  );
  assert.match(
    controllerSource,
    /subscribed = false;[\s\S]*unsubscribe\(\);[\s\S]*status\.clear\(sessionID\)/,
    "controller cleanup must close the stream before clearing status",
  );
});

test("deferred submit keeps follow-up text and attachment counts authoritative", () => {
  assert.match(composerSource, /settleSubmittedComposerText\(current, submittedText\)/);
  assert.doesNotMatch(composerSource, /setAttachmentCount\(0\)/);
});

test("active session composer floats without consuming conversation layout", () => {
  assert.match(composerSource, /new ResizeObserver/);
  assert.match(
    composerSource,
    /if \(!canSend \|\| !overlayNode\) \{[\s\S]*onClearanceChange\(0\);[\s\S]*return;/,
  );
  assert.match(
    sessionSource,
    /<ConversationContent[\s\S]*style=\{\{[\s\S]*paddingBottom:/,
  );
  assert.match(
    sessionSource,
    /<ConversationScrollButton[\s\S]*style=\{\{[\s\S]*bottom:/,
  );
  assert.match(
    sessionSource,
    /<ConversationClearanceFollower clearance=\{effectiveClearance\}/,
  );
  assert.match(
    sessionSource,
    /clearance > previousClearance\.current[\s\S]*scrollToBottom\(\{ animation: "instant" \}\)/,
  );
  assert.match(
    sessionSource,
    /canSend \? composerClearance : 0/,
  );
  assert.match(
    sessionSource,
    /<ConversationContent[\s\S]*max-w-\[808px\]/,
    "desktop transcript content bounds should align with the 760px composer after padding",
  );
  assert.match(
    composerSource,
    /data-testid="session-composer-overlay"/,
  );
  assert.match(
    composerSource,
    /pointer-events-none absolute inset-0[\s\S]*items-end/,
  );
  assert.match(
    composerSource,
    /className="flex max-h-full w-full flex-col overflow-visible px-4 md:px-6"/,
    "the composer frame must let the negative top fade render outside its measured height",
  );
  assert.match(composerSource, /data-testid="session-composer-obstruction"/);
  assert.match(
    composerSource,
    /data-testid="session-composer-fade"[\s\S]*absolute[\s\S]*inset-x-0[\s\S]*-top-12[\s\S]*h-12[\s\S]*bg-linear-to-b/,
    "the fade should be local to the composer width and live only above it",
  );
  const overlay = composerSource.match(
    /data-testid="session-composer-overlay"[\s\S]*?data-testid="session-composer-obstruction"/,
  )?.[0];
  assert.ok(overlay);
  assert.doesNotMatch(
    overlay,
    /bg-linear-to-b|to-background/,
    "the full-width overlay must not paint over the scrollbar or rounded prompt corners",
  );
  assert.match(
    composerSource,
    /pb-\[max\(0\.75rem,env\(safe-area-inset-bottom\)\)\][\s\S]*md:pb-\[max\(1\.25rem,env\(safe-area-inset-bottom\)\)\]/,
  );
  assert.match(composerSource, /data-testid="session-composer-stack"/);
  assert.match(
    composerSource,
    /pointer-events-auto[\s\S]*min-h-0[\s\S]*overflow-hidden/,
  );
  assert.match(
    composerSource,
    /<PromptInputTextarea[\s\S]*className="max-h-\[min\(12rem,30dvh\)\]"/,
  );
  assert.match(composerSource, /safe-area-inset-bottom/);
  assert.match(
    composerSource,
    /<Separator[\s\S]*className="h-4 self-center"[\s\S]*orientation="vertical"/,
  );
  assert.doesNotMatch(composerSource, /max-h-\[calc\(100dvh_/);
  assert.doesNotMatch(
    composerSource,
    /shrink-0 border-t bg-background\/92/,
  );
});

test("prompt input uses a floating surface and one border-only focus state", () => {
  assert.match(
    promptInputSource,
    /<InputGroup[\s\S]*rounded-\[16px\][\s\S]*shadow-\[var\(--shadow-lg\)\]/,
  );
  assert.match(
    promptInputSource,
    /has-\[\[data-slot=input-group-control\]:focus-visible\]:border-ring/,
  );
  assert.match(
    promptInputSource,
    /has-\[\[data-slot=input-group-control\]:focus-visible\]:ring-0/,
  );
  assert.match(
    promptInputSource,
    /has-\[\[data-slot=input-group-control\]:focus-visible\]:ring-offset-0/,
  );
  assert.doesNotMatch(
    composerSource,
    /variant=\{isStop \? "outline" : "default"\}/,
  );
});

test("queued inputs share one translucent floating surface", () => {
  assert.match(queuedSource, /data-testid="queued-input-stack"/);
  assert.match(queuedSource, /bg-background\/80/);
  assert.match(queuedSource, /backdrop-blur-xl/);
  assert.match(queuedSource, /shadow-\[var\(--shadow-md\)\]/);
  assert.match(
    queuedSource,
    /max-h-\[min\(14rem,30dvh\)\][\s\S]*min-h-0[\s\S]*shrink[\s\S]*flex-col/,
  );
  assert.match(queuedSource, /min-h-0 flex-1[\s\S]*overflow-y-auto/);
  assert.match(queuedSource, /divide-y/);
  assert.doesNotMatch(queuedSource, /bg-card\/90/);
});
