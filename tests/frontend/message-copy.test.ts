import assert from "node:assert/strict";
import test from "node:test";
import {
  COMPACT_COPIED_TOOLTIP,
  copyButtonDefaultTooltipMode,
  copyButtonTooltip,
  compactSummaryText,
  messageGroupCanCopy,
  messageGroupCopyText,
} from "../../frontend/src/lib/message-copy.ts";

test("compactSummaryText extracts the persisted compact summary", () => {
  assert.equal(
    compactSummaryText(
      "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\nold decisions",
    ),
    "old decisions",
  );
});

test("messageGroupCopyText joins copyable text blocks", () => {
  assert.equal(
    messageGroupCopyText({
      key: "m1",
      role: "user",
      pending: false,
      units: [
        { kind: "text", block: { type: "text", text: "first" } },
        { kind: "text", block: { type: "text", text: "second" } },
      ],
    }),
    "first\n\nsecond",
  );
});

test("messageGroupCopyText skips redacted reasoning internals", () => {
  const group = {
    key: "a-redacted",
    role: "assistant" as const,
    pending: false,
    units: [
      {
        kind: "reasoning" as const,
        block: {
          type: "reasoning" as const,
          text: "**Clarifying user request**",
          content: "encrypted hidden reasoning",
          redacted: true,
        },
      },
      {
        kind: "tool" as const,
        use: {
          type: "tool_use" as const,
          tool_use_id: "tu1",
          tool_name: "read",
          input: {},
        },
        result: null,
      },
    ],
  };

  assert.equal(messageGroupCopyText(group), "");
  assert.equal(messageGroupCanCopy(group), false);
});

test("messageGroupCopyText skips visible reasoning without content", () => {
  const group = {
    key: "a-visible-reasoning",
    role: "assistant" as const,
    pending: false,
    units: [
      {
        kind: "reasoning" as const,
        block: {
          type: "reasoning" as const,
          text: "visible process",
        },
      },
    ],
  };
  assert.equal(messageGroupCopyText(group), "");
  assert.equal(messageGroupCanCopy(group), false);
});

test("messageGroupCopyText copies text but not visible reasoning", () => {
  assert.equal(
    messageGroupCopyText({
      key: "a-reasoning-and-text",
      role: "assistant",
      pending: false,
      units: [
        {
          kind: "reasoning",
          block: { type: "reasoning", text: "visible process" },
        },
        { kind: "text", block: { type: "text", text: "visible answer" } },
      ],
    }),
    "visible answer",
  );
});

test("messageGroupCopyText keeps visible text next to redacted reasoning", () => {
  assert.equal(
    messageGroupCopyText({
      key: "a-mixed",
      role: "assistant",
      pending: false,
      units: [
        {
          kind: "reasoning",
          block: {
            type: "reasoning",
            text: "hidden reasoning",
            redacted: true,
          },
        },
        { kind: "text", block: { type: "text", text: "visible answer" } },
      ],
    }),
    "visible answer",
  );
});

test("messageGroupCanCopy allows assistant text messages", () => {
  assert.equal(
    messageGroupCanCopy({
      key: "a1",
      role: "assistant",
      pending: false,
      units: [{ kind: "text", block: { type: "text", text: "answer" } }],
    }),
    true,
  );
});

test("messageGroupCanCopy skips compact markers", () => {
  assert.equal(
    messageGroupCanCopy({
      key: "c1",
      role: "system",
      kind: "compact",
      pending: false,
      units: [{ kind: "text", block: { type: "text", text: "summary" } }],
    }),
    false,
  );
});

test("messageGroupCanCopy skips external event messages", () => {
  for (const kind of ["mcp_event", "observation"]) {
    assert.equal(
      messageGroupCanCopy({
        key: kind,
        role: "user",
        kind,
        pending: false,
        units: [{ kind: "text", block: { type: "text", text: "event body" } }],
      }),
      false,
    );
  }
});

test("copyButtonTooltip supports copied-only compact feedback", () => {
  const args = {
    mode: "copied-only" as const,
    copiedTooltip: COMPACT_COPIED_TOOLTIP,
    idleTooltip: "Copy compacted context",
  };

  assert.equal(
    copyButtonTooltip({ ...args, copied: false }),
    COMPACT_COPIED_TOOLTIP,
  );
  assert.equal(
    copyButtonTooltip({ ...args, copied: true }),
    "compacted content copied",
  );
});

test("copyButtonTooltip can disable message copy tooltips", () => {
  const args = {
    mode: "none" as const,
    copiedTooltip: "Copied to clipboard",
    idleTooltip: "Copy message",
  };

  assert.equal(copyButtonTooltip({ ...args, copied: false }), undefined);
  assert.equal(copyButtonTooltip({ ...args, copied: true }), undefined);
});

test("copyButtonDefaultTooltipMode disables icon-only copy tooltips", () => {
  const mode = copyButtonDefaultTooltipMode({ hasVisibleLabel: false });
  const args = {
    mode,
    copiedTooltip: "Copied to clipboard",
    idleTooltip: "Copy event content",
  };

  assert.equal(mode, "none");
  assert.equal(copyButtonTooltip({ ...args, copied: false }), undefined);
  assert.equal(copyButtonTooltip({ ...args, copied: true }), undefined);
});

test("copyButtonDefaultTooltipMode keeps tooltip for visible-label controls", () => {
  assert.equal(
    copyButtonDefaultTooltipMode({ hasVisibleLabel: true }),
    "always",
  );
});
