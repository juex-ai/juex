import assert from "node:assert/strict";
import test from "node:test";

import {
  messageGroupShouldShowModel,
  messagesToGroups,
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

  const groups = messagesToGroups(messages);

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
    ]),
    [
      ["tool-1", "memory_write", "first"],
      ["tool-2", "memory_write", "second"],
      ["tool-3", "update_goal", "third"],
    ],
  );
});

test("messageGroupShouldShowModel only shows normal assistant model labels", () => {
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
