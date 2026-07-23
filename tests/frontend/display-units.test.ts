import assert from "node:assert/strict";
import test from "node:test";

import {
  messagesToGroups,
  toolState,
} from "../../frontend/src/lib/display-units.ts";
import {
  messageGroupCanCopy,
  messageGroupCopyText,
} from "../../frontend/src/lib/message-copy.ts";
import type { Message } from "../../frontend/src/types.ts";

test("messagesToGroups normalizes legacy text blocks without text", () => {
  const messages = [
    {
      id: "legacy-empty-text",
      role: "assistant",
      blocks: [{ type: "text" }],
    },
  ] as unknown as Message[];

  const groups = messagesToGroups(messages);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].units.length, 1);
  assert.deepEqual(groups[0].units[0], {
    kind: "text",
    block: { type: "text", text: "" },
  });
  assert.equal(messageGroupCopyText(groups[0]), "");
  assert.equal(messageGroupCanCopy(groups[0]), false);
});

test("messagesToGroups folds contiguous assistant tools into a batch paired by id", () => {
  const messages: Message[] = [
    {
      id: "assistant-tools",
      role: "assistant",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "memory_write",
          input: { key: "a" },
        },
        {
          type: "tool_use",
          tool_use_id: "tool-2",
          tool_name: "memory_write",
          input: { key: "b" },
        },
        {
          type: "tool_use",
          tool_use_id: "tool-3",
          tool_name: "update_goal",
          input: { status: "complete" },
        },
      ],
    },
    {
      id: "tool-results",
      role: "user",
      blocks: [
        { type: "tool_result", tool_use_id: "tool-2", content: "second" },
        { type: "tool_result", tool_use_id: "tool-1", content: "first" },
        { type: "tool_result", tool_use_id: "tool-3", content: "third" },
      ],
    },
  ];

  const groups = messagesToGroups(messages, [
    {
      tool_use_id: "tool-1",
      name: "memory_write",
      state: "errored",
      started_at: "",
      updated_at: "",
    },
  ]);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].units.length, 1);
  const unit = groups[0].units[0];
  assert.equal(unit.kind, "tool_batch");
  if (unit.kind !== "tool_batch") return;
  assert.deepEqual(
    unit.tools.map((tool) => [
      tool.use?.tool_use_id,
      tool.use?.tool_name,
      tool.result?.content,
      tool.state,
    ]),
    [
      ["tool-1", "memory_write", "first", "errored"],
      ["tool-2", "memory_write", "second", undefined],
      ["tool-3", "update_goal", "third", undefined],
    ],
  );
});

test("toolState prefers the authoritative runtime lifecycle", () => {
  assert.equal(toolState(null, null, "requested"), "input-streaming");
  assert.equal(toolState(null, null, "running"), "input-available");
  assert.equal(toolState(null, null, "streaming"), "input-available");
  assert.equal(toolState(null, null, "completed"), "output-available");
  assert.equal(toolState(null, null, "errored"), "output-error");
});

test("messagesToGroups marks historical orphan tools errored after status loads", () => {
  const messages: Message[] = [
    {
      id: "interrupted-tool",
      role: "assistant",
      turn_id: "old-turn",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "exec_command",
          input: { cmd: "sleep 10" },
        },
      ],
    },
  ];

  const groups = messagesToGroups(messages, [], {
    runtimeStatusLoaded: true,
  });
  const unit = groups[0].units[0];
  assert.equal(unit.kind, "tool");
  if (unit.kind !== "tool") return;
  assert.equal(unit.state, "errored");
  assert.equal(toolState(unit.use, unit.result, unit.state), "output-error");
});

test("messagesToGroups keeps an unresolved tool active only in the active turn", () => {
  const messages: Message[] = [
    {
      id: "old-tool",
      role: "assistant",
      turn_id: "old-turn",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-old",
          tool_name: "exec_command",
        },
      ],
    },
    {
      id: "current-tool",
      role: "assistant",
      turn_id: "active-turn",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-current",
          tool_name: "exec_command",
        },
      ],
    },
  ];

  const groups = messagesToGroups(messages, [], {
    runtimeStatusLoaded: true,
    activeTurnID: "active-turn",
  });
  const oldUnit = groups[0].units[0];
  const currentUnit = groups[1].units[0];
  assert.equal(oldUnit.kind, "tool");
  assert.equal(currentUnit.kind, "tool");
  if (oldUnit.kind !== "tool" || currentUnit.kind !== "tool") return;
  assert.equal(oldUnit.state, "errored");
  assert.equal(currentUnit.state, undefined);
});

test("messagesToGroups does not override a delayed tool result", () => {
  const messages: Message[] = [
    {
      id: "tool-use",
      role: "assistant",
      turn_id: "old-turn",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "exec_command",
        },
      ],
    },
    {
      id: "tool-result",
      role: "user",
      turn_id: "old-turn",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "tool-1",
          content: "done",
        },
      ],
    },
  ];

  const groups = messagesToGroups(messages, [], {
    runtimeStatusLoaded: true,
  });
  const unit = groups[0].units[0];
  assert.equal(unit.kind, "tool");
  if (unit.kind !== "tool") return;
  assert.equal(unit.state, undefined);
  assert.equal(toolState(unit.use, unit.result, unit.state), "output-available");
});

test("messagesToGroups keeps image-only messages as image units", () => {
  const messages: Message[] = [
    {
      id: "image-only",
      role: "user",
      blocks: [
        {
          type: "image",
          media: {
            artifact_path: ".juex/artifacts/media/s/image.png",
            media_type: "image/png",
            sha256: "abc",
            original_bytes: 12,
            width: 2,
            height: 3,
          },
        },
      ],
    },
  ];

  const groups = messagesToGroups(messages);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].units.length, 1);
  assert.deepEqual(groups[0].units[0], {
    kind: "image",
    block: messages[0].blocks?.[0],
  });
  assert.equal(messageGroupCopyText(groups[0]), "");
  assert.equal(messageGroupCanCopy(groups[0]), false);
});

test("messagesToGroups preserves canonical mixed text and image order", () => {
  const messages: Message[] = [
    {
      id: "mixed-image",
      role: "user",
      blocks: [
        { type: "text", text: "before" },
        {
          type: "image",
          media: {
            artifact_path: ".juex/artifacts/media/s/image.png",
            media_type: "image/png",
            sha256: "abc",
            original_bytes: 12,
            width: 2,
            height: 3,
          },
        },
        { type: "text", text: "after" },
      ],
    },
  ];

  const groups = messagesToGroups(messages);

  assert.deepEqual(
    groups[0].units.map((unit) => unit.kind),
    ["text", "image", "text"],
  );
});
