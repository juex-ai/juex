import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  formatRuntimeToolSchema,
  runtimeToolGroupLabel,
  runtimeToolParameters,
  runtimeToolTimeoutLabel,
} from "../../frontend/src/lib/runtime-tool-catalog.ts";

test("runtimeToolGroupLabel maps the builtin group taxonomy", () => {
  assert.deepEqual(
    [
      "file",
      "chunked_write",
      "shell",
      "search",
      "skill",
      "memory",
      "session_state",
      "observable",
    ].map(runtimeToolGroupLabel),
    [
      "File",
      "Chunked Write",
      "Shell",
      "Search",
      "Skill",
      "Memory",
      "Session State",
      "Observable",
    ],
  );
  assert.equal(runtimeToolGroupLabel("custom_group"), "Custom Group");
  assert.equal(runtimeToolGroupLabel(null), "Other");
});

test("runtimeToolTimeoutLabel explains bounded and tool-managed timeouts", () => {
  assert.equal(
    runtimeToolTimeoutLabel({ mode: "bounded", seconds: 60 }),
    "60s timeout",
  );
  assert.equal(
    runtimeToolTimeoutLabel({ mode: "disabled", seconds: 0 }),
    "tool managed",
  );
});

test("runtimeToolTimeoutLabel falls back safely for unknown timeout metadata", () => {
  assert.equal(runtimeToolTimeoutLabel(undefined), "unknown timeout");
  assert.equal(
    runtimeToolTimeoutLabel({ mode: "bounded", seconds: 0 }),
    "unknown timeout",
  );
  assert.equal(
    runtimeToolTimeoutLabel({ mode: "future", seconds: 30 }),
    "unknown timeout",
  );
});

test("runtimeToolParameters sorts top-level properties and preserves required descriptions", () => {
  assert.deepEqual(
    runtimeToolParameters({
      properties: {
        zeta: { description: "Last value", type: "string" },
        alpha: { description: "First value", type: "integer" },
      },
      required: ["zeta"],
      type: "object",
    }),
    [
      {
        description: "First value",
        name: "alpha",
        required: false,
        type: "integer",
      },
      {
        description: "Last value",
        name: "zeta",
        required: true,
        type: "string",
      },
    ],
  );
});

test("runtimeToolParameters formats primitive, enum, array, object, and union types", () => {
  const rows = runtimeToolParameters({
    properties: {
      primitive: { type: "boolean" },
      choice: { enum: ["fast", "safe"], type: "string" },
      list: { items: { type: "integer" }, type: "array" },
      nested: {
        properties: { child: { type: "number" } },
        type: "object",
      },
      nullable: { type: ["string", "null"] },
      one: { oneOf: [{ type: "string" }, { type: "number" }] },
      any: { anyOf: [{ type: "boolean" }, { type: "null" }] },
    },
  });
  const types = Object.fromEntries(rows.map((row) => [row.name, row.type]));

  assert.deepEqual(types, {
    any: "boolean | null",
    choice: 'string enum ("fast" | "safe")',
    list: "array<integer>",
    nested: "object",
    nullable: "string | null",
    one: "string | number",
    primitive: "boolean",
  });
});

test("runtimeToolParameters returns an empty list for absent or malformed properties", () => {
  assert.deepEqual(runtimeToolParameters(undefined), []);
  assert.deepEqual(runtimeToolParameters({}), []);
  assert.deepEqual(runtimeToolParameters({ properties: null }), []);
  assert.deepEqual(runtimeToolParameters({ properties: [] }), []);
  assert.deepEqual(runtimeToolParameters({ properties: "invalid" }), []);
});

test("runtimeToolParameters never throws for hostile display data", () => {
  const schema = Object.create(null) as Record<string, unknown>;
  Object.defineProperty(schema, "properties", {
    get() {
      throw new Error("untrusted getter");
    },
  });

  assert.doesNotThrow(() => runtimeToolParameters(schema));
  assert.deepEqual(runtimeToolParameters(schema), []);
  assert.doesNotThrow(() => runtimeToolParameters(Symbol("schema")));
});

test("formatRuntimeToolSchema emits stable recursively sorted pretty JSON", () => {
  assert.equal(
    formatRuntimeToolSchema({
      zeta: 1,
      alpha: { zulu: true, bravo: [3, { y: 2, x: 1 }] },
    }),
    `{
  "alpha": {
    "bravo": [
      3,
      {
        "x": 1,
        "y": 2
      }
    ],
    "zulu": true
  },
  "zeta": 1
}`,
  );
});

test("formatRuntimeToolSchema keeps circular and unsupported inputs readable", () => {
  const circular: Record<string, unknown> = { name: "root" };
  circular.self = circular;

  assert.match(formatRuntimeToolSchema(circular), /"self": "\[Circular\]"/);
  assert.equal(formatRuntimeToolSchema(1n), '"[BigInt: 1]"');
  assert.equal(formatRuntimeToolSchema(() => undefined), '"[Function]"');

  const hostile = Object.create(null) as Record<string, unknown>;
  Object.defineProperty(hostile, "value", {
    enumerable: true,
    get() {
      throw new Error("untrusted getter");
    },
  });
  assert.doesNotThrow(() => formatRuntimeToolSchema(hostile));
  assert.match(formatRuntimeToolSchema(hostile), /Unable to format schema/);
});

test("runtime tool catalog helpers remain independent of React and the DOM", () => {
  const source = readFileSync(
    new URL("../../frontend/src/lib/runtime-tool-catalog.ts", import.meta.url),
    "utf8",
  );

  assert.doesNotMatch(source, /from\s+["']react["']/);
  assert.doesNotMatch(source, /\b(?:document|window)\b/);
});
