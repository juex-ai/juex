import assert from "node:assert/strict";
import test from "node:test";
import {
  assistantWorkItems,
  assistantWorkTitle,
  transcriptItemModelLabels,
} from "../../frontend/src/lib/assistant-work-groups.ts";
import type {
  DisplayUnit,
  MessageGroup,
} from "../../frontend/src/lib/display-units.ts";

function reasoning(text: string): DisplayUnit {
  return {
    kind: "reasoning",
    block: { type: "reasoning", text },
  };
}

function tool(id: string, name: string): DisplayUnit {
  return {
    kind: "tool",
    use: {
      type: "tool_use",
      tool_use_id: id,
      tool_name: name,
      input: {},
    },
    result: null,
  };
}

function batch(...tools: Array<[id: string, name: string]>): DisplayUnit {
  return {
    kind: "tool_batch",
    tools: tools.map(([id, name]) => ({
      kind: "tool",
      use: {
        type: "tool_use",
        tool_use_id: id,
        tool_name: name,
        input: {},
      },
      result: null,
    })),
  };
}

function text(value: string): DisplayUnit {
  return { kind: "text", block: { type: "text", text: value } };
}

function assistant(
  key: string,
  units: DisplayUnit[],
  options: Partial<MessageGroup> & { createdAt?: string } = {},
): MessageGroup {
  return {
    key,
    role: "assistant",
    pending: false,
    units,
    ...options,
  } as MessageGroup;
}

test("assistantWorkItems folds process groups through final visible content", () => {
  const groups = [
    {
      key: "user",
      role: "user" as const,
      pending: false,
      units: [text("go")],
    },
    assistant("live-starter", [reasoning("plan"), tool("tu-1", "read")], {
      model: "model-a",
      createdAt: "2026-07-20T01:00:00Z",
    }),
    assistant("second", [
      reasoning("execute"),
      batch(
        ["tu-2", "exec_command"],
        ["tu-3", "exec_command"],
        ["tu-4", "update_notes"],
      ),
    ]),
    assistant(
      "final",
      [
        reasoning("summarize"),
        tool("tu-5", "read"),
        text("  final answer  "),
        {
          kind: "image",
          block: {
            type: "image",
            media: { id: "img-1", mime_type: "image/png" },
          },
        },
      ],
      { createdAt: "2026-07-20T02:20:15Z" },
    ),
  ];

  const items = assistantWorkItems(groups, { tailActive: false });
  assert.equal(items.length, 2);
  assert.equal(items[0].kind, "message");
  const work = items[1];
  assert.equal(work.kind, "assistant_work");
  if (work.kind !== "assistant_work") return;
  assert.equal(work.key, "assistant-work:tu-1");
  assert.equal(work.phase, "completed");
  assert.equal(work.model, "model-a");
  assert.equal(work.toolCount, 5);
  assert.deepEqual(work.latestTools, [{ name: "read", count: 1 }]);
  assert.deepEqual(
    work.processGroups.flatMap((group) =>
      group.units.map((unit) => unit.kind),
    ),
    ["reasoning", "tool", "reasoning", "tool_batch", "reasoning", "tool"],
  );
  assert.deepEqual(
    work.contentGroup?.units.map((unit) => unit.kind),
    ["text", "image"],
  );
  assert.equal(
    assistantWorkTitle(work),
    "Worked for 1h 20min 15s, called 5 tools",
  );
});

test("running title follows the latest single or parallel tool-bearing group", () => {
  const single = assistantWorkItems(
    [assistant("starter", [reasoning("x"), tool("tu-1", "exec_command")])],
    { tailActive: true },
  )[0];
  assert.equal(single.kind, "assistant_work");
  if (single.kind !== "assistant_work") return;
  assert.equal(
    assistantWorkTitle(single),
    "Working with tool: exec_command",
  );

  const parallel = assistantWorkItems(
    [
      assistant("starter", [reasoning("x"), tool("tu-1", "read")]),
      assistant("next", [
        batch(
          ["tu-2", "exec_command"],
          ["tu-3", "exec_command"],
          ["tu-4", "exec_command"],
          ["tu-5", "update_notes"],
        ),
      ]),
    ],
    { tailActive: true },
  )[0];
  assert.equal(parallel.kind, "assistant_work");
  if (parallel.kind !== "assistant_work") return;
  assert.equal(
    assistantWorkTitle(parallel),
    "Working with tools: 3 exec_command, update_notes",
  );
});

test("inactive incomplete tail and interrupted buffers flush original messages", () => {
  const starter = assistant("starter", [
    reasoning("x"),
    tool("tu-1", "read"),
  ]);
  assert.deepEqual(
    assistantWorkItems([starter], { tailActive: false }).map(
      (item) => item.kind,
    ),
    ["message"],
  );

  const interrupted = assistantWorkItems(
    [
      starter,
      {
        key: "user",
        role: "user",
        pending: false,
        units: [text("stop")],
      },
    ],
    { tailActive: true },
  );
  assert.deepEqual(
    interrupted.map((item) => item.kind),
    ["message", "message"],
  );
});

test("only reasoning plus tool starts a group and whitespace is ignorable", () => {
  for (const units of [
    [reasoning("x"), text("answer")],
    [reasoning("x")],
    [tool("tu-1", "read")],
  ]) {
    assert.equal(
      assistantWorkItems([assistant("candidate", units)], {
        tailActive: true,
      })[0].kind,
      "message",
    );
  }

  const item = assistantWorkItems(
    [
      assistant("starter", [
        text(" \n "),
        reasoning("x"),
        tool("tu-1", "read"),
      ]),
      assistant("final", [text("answer")]),
    ],
    { tailActive: false },
  )[0];
  assert.equal(item.kind, "assistant_work");
});

test("effective model spans unknown but splits when a different model appears", () => {
  const modelA = assistant(
    "a",
    [reasoning("a"), tool("tu-a", "read")],
    { model: "model-a" },
  );
  const unknown = assistant("unknown", [reasoning("unknown")]);
  const same = assistant("same", [text("same answer")], { model: "model-a" });
  const sameItems = assistantWorkItems([modelA, unknown, same], {
    tailActive: false,
  });
  assert.equal(sameItems.length, 1);
  assert.equal(sameItems[0].kind, "assistant_work");

  const different = assistant(
    "different",
    [reasoning("b"), tool("tu-b", "write")],
    { model: "model-b" },
  );
  const splitItems = assistantWorkItems([modelA, unknown, different], {
    tailActive: true,
  });
  assert.deepEqual(
    splitItems.map((item) => item.kind),
    ["message", "message", "assistant_work"],
  );
});

test("stable tool identity survives running completion and live history keys", () => {
  const running = assistantWorkItems(
    [assistant("live-key", [reasoning("x"), tool("tu-1", "read")])],
    { tailActive: true },
  )[0];
  const completed = assistantWorkItems(
    [
      assistant("msg-history-key", [
        reasoning("x"),
        tool("tu-1", "read"),
      ]),
      assistant("msg-final", [text("done")]),
    ],
    { tailActive: false },
  )[0];
  assert.equal(running.key, "assistant-work:tu-1");
  assert.equal(completed.key, running.key);
});

test("duration falls back for missing, invalid, or reverse timestamps", () => {
  for (const [start, end] of [
    [undefined, "2026-07-20T01:00:01Z"],
    ["invalid", "2026-07-20T01:00:01Z"],
    ["2026-07-20T01:00:02Z", "2026-07-20T01:00:01Z"],
  ]) {
    const item = assistantWorkItems(
      [
        assistant(
          "starter",
          [reasoning("x"), tool("tu-1", "read")],
          { createdAt: start },
        ),
        assistant("final", [text("done")], { createdAt: end }),
      ],
      { tailActive: false },
    )[0];
    assert.equal(item.kind, "assistant_work");
    if (item.kind !== "assistant_work") continue;
    assert.equal(assistantWorkTitle(item), "Worked, called 1 tool");
  }
});

test("page-head prefixes stay original until the starter is prepended", () => {
  const prefix = assistant("middle", [reasoning("continue")]);
  const final = assistant("final", [text("done")]);
  assert.deepEqual(
    assistantWorkItems([prefix, final], { tailActive: false }).map(
      (item) => item.kind,
    ),
    ["message", "message"],
  );
  assert.deepEqual(
    assistantWorkItems(
      [
        assistant("starter", [
          reasoning("start"),
          tool("tu-1", "read"),
        ]),
        prefix,
        final,
      ],
      { tailActive: false },
    ).map((item) => item.kind),
    ["assistant_work"],
  );
});

test("model labels align with final transcript items", () => {
  const items = assistantWorkItems(
    [
      assistant(
        "starter",
        [reasoning("x"), tool("tu-1", "read")],
        { model: "model-a" },
      ),
      assistant("final", [text("done")], { model: "model-a" }),
      assistant("next", [text("plain")], { model: "model-b" }),
    ],
    { tailActive: false },
  );
  assert.deepEqual(transcriptItemModelLabels(items), [
    "model-a",
    "model-b",
  ]);
});
