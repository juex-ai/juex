import assert from "node:assert/strict";
import test from "node:test";
import {
  externalEventBodyClassName,
  externalEventCopyClassName,
  externalEventRowClassName,
  messageContentBaseClassName,
  messageContentRoleClassName,
  messageResponseClassName,
  processDisclosureClassName,
  processDisclosureSummaryClassName,
  processStatusDotClassName,
  thinkingDisclosureBodyClassName,
  thinkingDisclosureSummaryClassName,
} from "../../frontend/src/lib/message-rendering.ts";

test("messageResponseClassName preserves explicit paragraph newlines", () => {
  assert.match(messageResponseClassName(), /\[&_p\]:whitespace-pre-wrap/);
});

test("user message chrome uses a weak card treatment", () => {
  const base = messageContentBaseClassName();
  const user = messageContentRoleClassName("user");

  assert.match(base, /text-\[14\.5px\]/);
  assert.match(base, /bg-card/);
  assert.match(base, /border-border/);
  assert.match(base, /text-card-foreground/);
  assert.match(user, /group-\[\.is-user\]:ml-auto/);
  assert.doesNotMatch(user, /bg-juex-user/);
  assert.doesNotMatch(user, /text-juex-user-foreground/);
});

test("assistant message chrome keeps card treatment", () => {
  const base = messageContentBaseClassName();
  const assistant = messageContentRoleClassName("assistant");

  assert.match(base, /bg-card/);
  assert.match(base, /border-border/);
  assert.match(base, /text-card-foreground/);
  assert.match(assistant, /group-\[\.is-assistant\]:rounded-tl-\[6px\]/);
});

test("external event row renders as inline text instead of a bubble", () => {
  const row = externalEventRowClassName();

  assert.match(row, /flex/);
  assert.match(row, /items-center/);
  assert.match(row, /text-juex-gold-900/);
  assert.doesNotMatch(row, /rounded/);
  assert.doesNotMatch(row, /border/);
  assert.doesNotMatch(row, /bg-juex-gold/);
  assert.doesNotMatch(row, /shadow/);
});

test("external event expanded body owns a hover-revealed copy action", () => {
  const body = externalEventBodyClassName();
  const copy = externalEventCopyClassName();

  assert.match(body, /relative/);
  assert.match(body, /group/);
  assert.match(body, /border-t/);
  assert.match(copy, /absolute/);
  assert.match(copy, /right-2/);
  assert.match(copy, /top-2/);
  assert.match(copy, /opacity-0/);
  assert.match(copy, /group-hover:opacity-100/);
  assert.match(copy, /group-focus-within:opacity-100/);
});

test("process disclosure chrome does not look like a bracketed bubble", () => {
  const root = processDisclosureClassName();
  const summary = processDisclosureSummaryClassName();

  assert.match(root, /w-full/);
  assert.doesNotMatch(root, /border-l/);
  assert.doesNotMatch(root, /rounded/);
  assert.doesNotMatch(root, /shadow/);
  assert.match(summary, /inline-flex/);
  assert.doesNotMatch(summary, /flex-1/);
});

test("process status dots are smaller while thinking has no dot contract", () => {
  const dot = processStatusDotClassName("done");
  const failedDot = processStatusDotClassName("failed");
  const thinking = thinkingDisclosureSummaryClassName();

  assert.match(dot, /size-\[5px\]/);
  assert.match(dot, /bg-juex-done/);
  assert.match(failedDot, /bg-juex-error/);
  assert.doesNotMatch(thinking, /bg-juex-done/);
  assert.doesNotMatch(thinking, /rounded-full/);
});

test("thinking disclosure uses muted title and direct body content", () => {
  const summary = thinkingDisclosureSummaryClassName();
  const body = thinkingDisclosureBodyClassName();

  assert.match(summary, /text-muted-foreground/);
  assert.match(summary, /inline-flex/);
  assert.doesNotMatch(summary, /text-juex-done/);
  assert.match(body, /text-foreground/);
  assert.doesNotMatch(body, /uppercase/);
});
