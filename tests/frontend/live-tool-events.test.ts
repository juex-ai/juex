import { strict as assert } from "node:assert";
import test from "node:test";

import { applyToolRequestedToMessages } from "../../frontend/src/lib/live-tool-events.ts";
import type { Message } from "../../frontend/src/types.ts";

test("applyToolRequestedToMessages adds a running tool block to a pending assistant", () => {
  const messages: Message[] = [
    { role: "user", turn_id: "t1", blocks: [{ type: "text", text: "run it" }] },
    { role: "assistant", turn_id: "t1", pending: true, blocks: [] },
  ];

  const next = applyToolRequestedToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "shell",
    input: { cmd: "sleep 10" },
    timeoutSeconds: 30,
  });

  const assistant = next[1];
  assert.equal(assistant.blocks?.length, 1);
  assert.deepEqual(assistant.blocks?.[0], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "shell",
    input: { cmd: "sleep 10" },
    timeout_seconds: 30,
  });
});

test("applyToolRequestedToMessages updates an existing tool block without duplication", () => {
  const messages: Message[] = [
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "shell",
          input: { cmd: "sleep 10" },
        },
      ],
    },
  ];

  const next = applyToolRequestedToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "shell",
    input: { cmd: "sleep 10" },
    timeoutSeconds: 45,
  });

  assert.equal(next[0].blocks?.length, 1);
  assert.deepEqual(next[0].blocks?.[0], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "shell",
    input: { cmd: "sleep 10" },
    timeout_seconds: 45,
  });
});

test("applyToolRequestedToMessages appends to a completed assistant message", () => {
  const messages: Message[] = [
    { role: "user", turn_id: "t1", blocks: [{ type: "text", text: "run it" }] },
    {
      role: "assistant",
      turn_id: "t1",
      pending: false,
      blocks: [{ type: "text", text: "I'll run it." }],
    },
  ];

  const next = applyToolRequestedToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "shell",
    input: { cmd: "sleep 10" },
    timeoutSeconds: 30,
  });

  assert.equal(next.length, 2);
  assert.deepEqual(next[1].blocks?.[1], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "shell",
    input: { cmd: "sleep 10" },
    timeout_seconds: 30,
  });
});
