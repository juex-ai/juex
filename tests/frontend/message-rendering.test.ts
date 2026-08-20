import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  externalEventCopyClassName,
  formatMessageSentAt,
  messageContentBaseClassName,
  messageContentUserClassName,
  messageResponseClassName,
  processDisclosureChevronClassName,
  processDisclosureClassName,
  processDisclosureSummaryClassName,
  processStatusDotClassName,
  thinkingDisclosureBodyClassName,
  thinkingDisclosureSummaryClassName,
  transcriptDisclosureBodyClassName,
  transcriptDisclosureRowClassName,
} from "../../frontend/src/lib/message-rendering.ts";

const transcriptSource = readFileSync(
  new URL(
    "../../frontend/src/components/session/SessionTranscript.tsx",
    import.meta.url,
  ),
  "utf8",
);

test("messageResponseClassName preserves explicit paragraph newlines", () => {
  assert.match(messageResponseClassName(), /\[&_p\]:whitespace-pre-wrap/);
});

test("formatMessageSentAt uses the browser local timezone", () => {
  const localTime = new Date(2026, 7, 20, 12, 8, 20);

  assert.equal(
    formatMessageSentAt(localTime.toISOString()),
    "0820 - 12:08:20",
  );
});

test("formatMessageSentAt omits missing and invalid timestamps", () => {
  assert.equal(formatMessageSentAt(undefined), undefined);
  assert.equal(formatMessageSentAt("not-a-timestamp"), undefined);
});

test("message sent time stays inside the copy action on the inner edge", () => {
  const start = transcriptSource.indexOf("function MessageMetaActions(");
  const end = transcriptSource.indexOf("function CopyTextButton(", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const action = transcriptSource.slice(start, end);

  assert.match(action, /copyText\?: string/);
  assert.match(action, /createdAt\?: string/);
  assert.match(action, /const sentTime = formatMessageSentAt\(createdAt\)/);
  assert.match(action, /align === "end" \? timeElement : null/);
  assert.match(action, /align === "start" \? timeElement : null/);
  assert.match(action, /<time[\s\S]*?dateTime=\{createdAt\}/);
  assert.match(action, /copyText \? \([\s\S]*?className="size-6/);
});

test("ordinary copy rows receive their message creation time", () => {
  const assistantStart = transcriptSource.indexOf(
    "function AssistantWorkGroupView(",
  );
  const assistantEnd = transcriptSource.indexOf(
    "function AssistantWorkContent(",
    assistantStart,
  );
  const defaultStart = transcriptSource.indexOf("function DefaultMessageGroup(");
  const defaultEnd = transcriptSource.indexOf(
    "function MessageImageGallery(",
    defaultStart,
  );
  const compactStart = transcriptSource.indexOf("function CompactGroup(");
  const compactEnd = transcriptSource.indexOf(
    "function PendingCompactGroup(",
    compactStart,
  );
  const assistant = transcriptSource.slice(assistantStart, assistantEnd);
  const normal = transcriptSource.slice(defaultStart, defaultEnd);
  const compact = transcriptSource.slice(compactStart, compactEnd);

  assert.match(
    assistant,
    /<MessageMetaActions[\s\S]*?copyText=\{canCopy \? copyText : undefined\}[\s\S]*?createdAt=\{content\?\.createdAt\}/,
  );
  assert.match(
    normal,
    /<MessageMetaActions[\s\S]*?copyText=\{canCopyMessage \? copyText : undefined\}[\s\S]*?createdAt=\{group\.createdAt\}/,
  );
  assert.match(
    compact,
    /<SlashCommandMessage[\s\S]*?text=\{compactCommand\.input\}[\s\S]*?createdAt=\{compactCommand\.submittedAt\}/,
  );
  assert.doesNotMatch(compact, /createdAt=\{group\.createdAt\}/);
  assert.doesNotMatch(normal, /\{canCopyMessage \? \([\s\S]*?<MessageMetaActions/);
});

test("user message chrome uses a weak card treatment", () => {
  const base = messageContentBaseClassName();
  const user = messageContentUserClassName();

  assert.match(base, /text-\[14\.5px\]/);
  assert.match(base, /bg-card/);
  assert.match(base, /border-border/);
  assert.match(base, /text-card-foreground/);
  assert.match(user, /group-\[\.is-user\]:ml-auto/);
  assert.match(user, /group-\[\.is-user\]:rounded-\[16px\]/);
  assert.match(user, /group-\[\.is-user\]:rounded-tr-md/);
  assert.doesNotMatch(user, /bg-juex-user/);
  assert.doesNotMatch(user, /text-juex-user-foreground/);
});

test("external event row renders as inline text instead of a bubble", () => {
  const row = transcriptDisclosureRowClassName("external");

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
  const body = transcriptDisclosureBodyClassName("external");
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

test("system notification disclosure uses the blue information ramp", () => {
  const row = transcriptDisclosureRowClassName("system");
  const body = transcriptDisclosureBodyClassName("system");

  assert.match(row, /text-juex-info/);
  assert.match(row, /dark:text-juex-info/);
  assert.doesNotMatch(row, /text-juex-error/);
  assert.doesNotMatch(row, /text-juex-gold/);
  assert.match(body, /border-juex-info/);
  assert.match(body, /bg-juex-info-bg/);
  assert.match(body, /max-h-\[15rem\]/);
  assert.match(body, /overflow-auto/);
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

test("all process disclosures start closed and only follow user toggles", () => {
  const start = transcriptSource.indexOf("function ProcessDisclosure(");
  assert.notEqual(start, -1);
  const nextDeclaration = transcriptSource
    .slice(start + 1)
    .search(/\n(?:export )?function /);
  assert.notEqual(nextDeclaration, -1);
  const end = start + 1 + nextDeclaration;
  const disclosure = transcriptSource.slice(start, end);

  assert.match(disclosure, /const \[isOpen, setIsOpen\] = useState\(false\)/);
  assert.match(disclosure, /open=\{isOpen\}/);
  assert.match(
    disclosure,
    /onToggle=\{\(event\) => setIsOpen\(event\.currentTarget\.open\)\}/,
  );
  assert.doesNotMatch(disclosure, /useEffect/);
  assert.doesNotMatch(disclosure, /setIsOpen\([^)]*status/);
});

test("system and model notices share a blue notification disclosure", () => {
  assert.match(
    transcriptSource,
    /system_notice: SystemNoticeGroup/,
  );

  const systemStart = transcriptSource.indexOf("function SystemNoticeGroup(");
  const modelStart = transcriptSource.indexOf("function ModelFallbackGroup(");
  const notificationStart = transcriptSource.indexOf(
    "function SystemNotificationMessage(",
  );
  const notificationEnd = transcriptSource.indexOf(
    "\nfunction ",
    notificationStart + 1,
  );
  assert.notEqual(systemStart, -1);
  assert.notEqual(modelStart, -1);
  assert.notEqual(notificationStart, -1);
  assert.notEqual(notificationEnd, -1);

  const system = transcriptSource.slice(systemStart, modelStart);
  const model = transcriptSource.slice(modelStart, notificationStart);
  const notification = transcriptSource.slice(
    notificationStart,
    notificationEnd,
  );

  assert.match(system, /formatSystemNotice\(text\)/);
  assert.match(system, /kind="system_notice"/);
  assert.match(model, /formatModelFallbackNotice\(text\)/);
  assert.match(model, /kind="model_fallback"/);
  assert.match(notification, /<details/);
  assert.match(notification, /<BellIcon/);
  assert.match(notification, /transcriptDisclosureRowClassName\("system"\)/);
  assert.match(notification, /transcriptDisclosureBodyClassName\("system"\)/);
  assert.match(notification, /max-w-\[min\(34rem,100%\)\]/);
  assert.match(notification, /aria-hidden="true"/);
  assert.match(notification, /data-system-notification-message/);
  assert.doesNotMatch(system, /<ProcessDisclosure/);
  assert.doesNotMatch(model, /<ProcessDisclosure/);
  assert.doesNotMatch(notification, /<Message from="user">/);
});

test("assistant work disclosure owns process rows and leaves content outside", () => {
  const start = transcriptSource.indexOf("function AssistantWorkGroupView(");
  const end = transcriptSource.indexOf("function AssistantWorkContent(", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const disclosure = transcriptSource.slice(start, end);

  assert.match(disclosure, /const \[isOpen, setIsOpen\] = useState\(false\)/);
  assert.match(disclosure, /assistantWorkTitle\(work\)/);
  assert.match(disclosure, /group\/work-row/);
  assert.match(disclosure, /work\.processGroups\.flatMap/);
  assert.match(disclosure, /<ThinkingProcessRow/);
  assert.match(disclosure, /<ToolBatchProcessRow/);
  assert.match(disclosure, /<ToolProcessRow/);
  assert.match(disclosure, /<AssistantWorkContent group=\{content\}/);
  assert.match(
    disclosure,
    /<MessageMetaActions[\s\S]*?copyText=\{canCopy \? copyText : undefined\}/,
  );
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
