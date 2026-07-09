import assert from "node:assert/strict";
import test from "node:test";
import {
  externalEventBodyClassName,
  externalEventCopyClassName,
  externalEventRowClassName,
  messageContentBaseClassName,
  messageContentRoleClassName,
  messageGroupModelLabels,
  messageGroupShouldShowModel,
  messageResponseClassName,
  processDisclosureChevronClassName,
  processDisclosureClassName,
  processDisclosureDefaultOpen,
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

test("message model labels only render for normal assistant messages", () => {
  assert.equal(
    messageGroupShouldShowModel({
      key: "assistant-normal",
      role: "assistant",
      pending: false,
      units: [],
      model: "gpt-test",
    }),
    true,
  );
  assert.equal(
    messageGroupShouldShowModel({
      key: "assistant-system",
      role: "assistant",
      kind: "system_status",
      pending: false,
      units: [],
      model: "gpt-test",
    }),
    false,
  );
  assert.equal(
    messageGroupShouldShowModel({
      key: "user-with-model",
      role: "user",
      pending: false,
      units: [],
      model: "gpt-test",
    }),
    false,
  );
  assert.equal(
    messageGroupShouldShowModel({
      key: "assistant-no-model",
      role: "assistant",
      pending: false,
      units: [],
    }),
    false,
  );
});

test("messageGroupModelLabels labels assistant model run starts", () => {
  const labels = messageGroupModelLabels([
    {
      key: "user-start",
      role: "user",
      pending: false,
      units: [],
    },
    {
      key: "assistant-a-1",
      role: "assistant",
      pending: false,
      units: [],
      model: "model-a",
    },
    {
      key: "assistant-a-2",
      role: "assistant",
      pending: false,
      units: [],
      model: "model-a",
    },
    {
      key: "assistant-b-1",
      role: "assistant",
      pending: false,
      units: [],
      model: "model-b",
    },
    {
      key: "assistant-b-2",
      role: "assistant",
      pending: false,
      units: [],
      model: "model-b",
    },
    {
      key: "user-break",
      role: "user",
      pending: false,
      units: [],
    },
    {
      key: "assistant-b-after-user",
      role: "assistant",
      pending: false,
      units: [],
      model: "model-b",
    },
    {
      key: "assistant-kind-break",
      role: "assistant",
      kind: "hook_event",
      pending: false,
      units: [],
      model: "model-b",
    },
    {
      key: "assistant-b-after-kind",
      role: "assistant",
      pending: false,
      units: [],
      model: "model-b",
    },
  ]);

  assert.deepEqual(labels, [
    undefined,
    "model-a",
    undefined,
    "model-b",
    undefined,
    undefined,
    "model-b",
    undefined,
    "model-b",
  ]);
});

test("external event row renders as inline text instead of a bubble", () => {
  const row = externalEventRowClassName();

  assert.match(row, /flex/);
  assert.match(row, /items-center/);
  assert.match(row, /text-juex-gold-900/);
  assert.match(row, /cursor-pointer/);
  assert.match(row, /list-none/);
  assert.match(row, /hover:text-/);
  assert.match(row, /focus-visible:ring/);
  assert.doesNotMatch(row, /rounded/);
  assert.doesNotMatch(row, /border/);
  assert.doesNotMatch(row, /bg-juex-gold/);
  assert.doesNotMatch(row, /shadow/);
});

test("external event expanded body scrolls inside a bordered area", () => {
  const body = externalEventBodyClassName();
  const copy = externalEventCopyClassName();

  assert.match(body, /relative/);
  assert.match(body, /group/);
  assert.match(body, /rounded/);
  assert.match(body, /border/);
  assert.match(body, /max-h-\[15rem\]/);
  assert.match(body, /overflow-auto/);
  assert.match(body, /leading-6/);
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
  assert.match(root, /group\/process-row/);
  assert.doesNotMatch(root, /border-l/);
  assert.doesNotMatch(root, /rounded/);
  assert.doesNotMatch(root, /shadow/);
  assert.match(summary, /inline-flex/);
  assert.doesNotMatch(summary, /flex-1/);
});

test("nested process disclosures only rotate their own chevrons", () => {
  const nested = processDisclosureClassName(true);
  const rootChevron = processDisclosureChevronClassName();
  const nestedChevron = processDisclosureChevronClassName(true);

  assert.match(nested, /group\/nested-process-row/);
  assert.doesNotMatch(nested, /group\/process-row\b/);
  assert.match(rootChevron, /group-open\/process-row:rotate-90/);
  assert.match(nestedChevron, /group-open\/nested-process-row:rotate-90/);
  assert.doesNotMatch(nestedChevron, /group-open\/process-row:rotate-90/);
});

test("process disclosures default closed for every status", () => {
  assert.equal(processDisclosureDefaultOpen(), false);
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
  assert.match(body, /max-h-\[15rem\]/);
  assert.match(body, /overflow-auto/);
  assert.match(body, /rounded/);
  assert.match(body, /border/);
  assert.match(body, /leading-6/);
  assert.match(body, /text-foreground/);
  assert.doesNotMatch(body, /uppercase/);
});
