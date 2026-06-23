import { strict as assert } from "node:assert";
import test from "node:test";

import {
  applyToolOutputDeltaToMessages,
  applyToolRequestedToMessages,
  applyToolResultToMessages,
} from "../../frontend/src/lib/live-tool-events.ts";
import { messagesToGroups } from "../../frontend/src/lib/display-units.ts";
import type { Message } from "../../frontend/src/types.ts";

test("applyToolRequestedToMessages adds a running tool block to a pending assistant", () => {
  const messages: Message[] = [
    { role: "user", turn_id: "t1", blocks: [{ type: "text", text: "run it" }] },
    { role: "assistant", turn_id: "t1", pending: true, blocks: [] },
  ];

  const next = applyToolRequestedToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    input: { cmd: "sleep 10" },
    timeoutSeconds: 30,
  });

  const assistant = next[1];
  assert.equal(assistant.blocks?.length, 1);
  assert.deepEqual(assistant.blocks?.[0], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "exec_command",
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
          tool_name: "exec_command",
          input: { cmd: "sleep 10" },
        },
      ],
    },
  ];

  const next = applyToolRequestedToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    input: { cmd: "sleep 10" },
    timeoutSeconds: 45,
  });

  assert.equal(next[0].blocks?.length, 1);
  assert.deepEqual(next[0].blocks?.[0], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "exec_command",
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
    toolName: "exec_command",
    input: { cmd: "sleep 10" },
    timeoutSeconds: 30,
  });

  assert.equal(next.length, 2);
  assert.deepEqual(next[1].blocks?.[1], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "exec_command",
    input: { cmd: "sleep 10" },
    timeout_seconds: 30,
  });
});

test("applyToolOutputDeltaToMessages appends a live tool result", () => {
  const messages: Message[] = [
    {
      role: "assistant",
      turn_id: "t1",
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

  const next = applyToolOutputDeltaToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    text: "pulling layer\n",
  });

  assert.equal(next.length, 2);
  assert.deepEqual(next[1], {
    role: "user",
    turn_id: "t1",
    blocks: [
      {
        type: "tool_result",
        tool_use_id: "tool-1",
        content: "pulling layer\n",
      },
    ],
  });
});

test("applyToolOutputDeltaToMessages creates a named placeholder for missed requests", () => {
  const next = applyToolOutputDeltaToMessages([], {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    text: "pulling layer\n",
  });

  assert.deepEqual(next, [
    {
      role: "assistant",
      turn_id: "t1",
      pending: true,
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "exec_command",
        },
      ],
    },
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "tool-1",
          content: "pulling layer\n",
        },
      ],
    },
  ]);
});

test("applyToolOutputDeltaToMessages updates an existing live tool result", () => {
  const messages: Message[] = [
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "tool-1",
          content: "first\n",
        },
      ],
    },
  ];

  const next = applyToolOutputDeltaToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    text: "second\n",
  });

  assert.equal(next.length, 1);
  assert.deepEqual(next[0].blocks, [
    {
      type: "tool_result",
      tool_use_id: "tool-1",
      content: "first\nsecond\n",
    },
  ]);
});

test("applyToolOutputDeltaToMessages preserves cumulative truncation counts", () => {
  const retained = "a".repeat(12000);
  const messages: Message[] = [
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "tool-1",
          content: `[live output truncated: 500 earlier characters omitted]\n${retained}`,
        },
      ],
    },
  ];

  const next = applyToolOutputDeltaToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    text: "bc",
  });

  const block = next[0].blocks?.[0];
  assert.equal(block?.type, "tool_result");
  assert.equal(
    block?.content,
    `[live output truncated: 502 earlier characters omitted]\n${"a".repeat(11998)}bc`,
  );
});

test("applyToolResultToMessages finalizes an existing live result without duplication", () => {
  const messages: Message[] = [
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "exec_command",
        },
      ],
    },
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "tool-1",
          content: "first\nsecond\n",
        },
      ],
    },
  ];

  const next = applyToolResultToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    content: "first\n",
  });

  assert.equal(next.length, 2);
  assert.deepEqual(next[1].blocks, [
    {
      type: "tool_result",
      tool_use_id: "tool-1",
      content: "first\nsecond\n",
    },
  ]);
});

test("applyToolResultToMessages enriches a streamed placeholder with timeout metadata", () => {
  const streamed = applyToolOutputDeltaToMessages([], {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    text: "first\n",
  });

  const next = applyToolResultToMessages(streamed, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    content: "first\n",
    timeoutSeconds: 30,
  });

  assert.deepEqual(next[0].blocks?.[0], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "exec_command",
    timeout_seconds: 30,
  });
  assert.equal(next[1].blocks?.length, 1);
});

test("applyToolResultToMessages leaves existing complete tool metadata untouched", () => {
  const messages: Message[] = [
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "tool-1",
          tool_name: "exec_command",
          input: { cmd: "printf hi" },
          timeout_seconds: 30,
        },
      ],
    },
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "tool-1",
          content: "hi",
        },
      ],
    },
  ];

  const next = applyToolResultToMessages(messages, {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    content: "hi",
    timeoutSeconds: 30,
  });

  assert.equal(next[0], messages[0]);
});

test("applyToolResultToMessages creates a named placeholder for missed completions", () => {
  const next = applyToolResultToMessages([], {
    turnID: "t1",
    toolUseID: "tool-1",
    toolName: "exec_command",
    content: "done\n",
    timeoutSeconds: 30,
  });

  assert.equal(next[0].role, "assistant");
  assert.deepEqual(next[0].blocks?.[0], {
    type: "tool_use",
    tool_use_id: "tool-1",
    tool_name: "exec_command",
    timeout_seconds: 30,
  });
  assert.deepEqual(next[1].blocks?.[0], {
    type: "tool_result",
    tool_use_id: "tool-1",
    content: "done\n",
  });
});

test("applyToolResultToMessages inserts after the matching later tool_use", () => {
  const messages: Message[] = [
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "old-tool",
          tool_name: "exec_command",
        },
      ],
    },
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "old-tool",
          content: "old result",
        },
      ],
    },
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "new-tool",
          tool_name: "create_goal",
        },
      ],
    },
  ];

  const next = applyToolResultToMessages(messages, {
    turnID: "t1",
    toolUseID: "new-tool",
    toolName: "create_goal",
    content: "new result",
  });

  assert.equal(next.length, 4);
  assert.deepEqual(next[1].blocks, [
    {
      type: "tool_result",
      tool_use_id: "old-tool",
      content: "old result",
    },
  ]);
  assert.deepEqual(next[3], {
    role: "user",
    turn_id: "t1",
    blocks: [
      {
        type: "tool_result",
        tool_use_id: "new-tool",
        content: "new result",
      },
    ],
  });

  const groups = messagesToGroups(next);
  assert.equal(groups.length, 2);
  const newUnit = groups[1].units[0];
  assert.equal(newUnit?.kind, "tool");
  if (newUnit?.kind === "tool") {
    assert.equal(newUnit.use?.tool_use_id, "new-tool");
    assert.equal(newUnit.result?.tool_use_id, "new-tool");
  }
});

test("applyToolOutputDeltaToMessages keeps live output after the matching later tool_use", () => {
  const messages: Message[] = [
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "old-tool",
          tool_name: "exec_command",
        },
      ],
    },
    {
      role: "user",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_result",
          tool_use_id: "old-tool",
          content: "old result",
        },
      ],
    },
    {
      role: "assistant",
      turn_id: "t1",
      blocks: [
        {
          type: "tool_use",
          tool_use_id: "streaming-tool",
          tool_name: "exec_command",
        },
      ],
    },
  ];

  const next = applyToolOutputDeltaToMessages(messages, {
    turnID: "t1",
    toolUseID: "streaming-tool",
    toolName: "exec_command",
    text: "live output\n",
  });

  assert.equal(next.length, 4);
  assert.deepEqual(next[3], {
    role: "user",
    turn_id: "t1",
    blocks: [
      {
        type: "tool_result",
        tool_use_id: "streaming-tool",
        content: "live output\n",
      },
    ],
  });
  const groups = messagesToGroups(next);
  const liveUnit = groups[1].units[0];
  assert.equal(liveUnit?.kind, "tool");
  if (liveUnit?.kind === "tool") {
    assert.equal(liveUnit.result?.content, "live output\n");
  }
});
