import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import {
  createObservable,
  runObservable,
} from "../../frontend/src/api.ts";

const requireFromFrontend = createRequire(
  new URL("../../frontend/package.json", import.meta.url),
);
const ts = requireFromFrontend("typescript") as typeof import("typescript");

const typesSource = readFileSync(
  new URL("../../frontend/src/types.ts", import.meta.url),
  "utf8",
);

test("ObservableCreateRequest is a tagged command or schedule union", () => {
  const sourceFile = ts.createSourceFile(
    "types.ts",
    typesSource,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
  );
  const declaration = sourceFile.statements.find(
    (statement): statement is import("typescript").TypeAliasDeclaration =>
      ts.isTypeAliasDeclaration(statement) &&
      statement.name.text === "ObservableCreateRequest",
  );

  assert.ok(declaration, "missing ObservableCreateRequest type alias");
  assert.ok(
    ts.isUnionTypeNode(declaration.type),
    "ObservableCreateRequest must be a discriminated union",
  );
  assert.equal(declaration.type.types.length, 2);

  const variants = declaration.type.types.map((variant) => variant.getText());
  const command = variants.find((variant) => /type:\s*"command"/.test(variant));
  const schedule = variants.find((variant) => /type:\s*"schedule"/.test(variant));
  assert.ok(command, "missing command variant");
  assert.match(command, /id\?:\s*string/);
  assert.match(command, /command_config:/);
  assert.match(command, /schedule_config\?:\s*never/);
  assert.ok(schedule, "missing schedule variant");
  assert.match(schedule, /id\?:\s*string/);
  assert.match(schedule, /schedule_config:/);
  assert.match(schedule, /command_config\?:\s*never/);
});

test("createObservable posts tagged command and schedule bodies unchanged", async () => {
  const originalFetch = globalThis.fetch;
  const bodies: unknown[] = [];
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    bodies.push(JSON.parse(String(init?.body)));
    return new Response(JSON.stringify({ id: "created", state: "running" }), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  try {
    await createObservable({
      id: "command-source",
      type: "command",
      command_config: {
        command: "lark-cli",
        streams: ["stdout"],
        batch: { interval_seconds: 10, max_chars: 1000 },
      },
    });
    await createObservable({
      id: "schedule-source",
      type: "schedule",
      schedule_config: {
        timezone: "Asia/Shanghai",
        interval: { every_seconds: 60 },
        observation: { content: "Prepare a work brief." },
      },
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(bodies, [
    {
      id: "command-source",
      type: "command",
      command_config: {
        command: "lark-cli",
        streams: ["stdout"],
        batch: { interval_seconds: 10, max_chars: 1000 },
      },
    },
    {
      id: "schedule-source",
      type: "schedule",
      schedule_config: {
        timezone: "Asia/Shanghai",
        interval: { every_seconds: 60 },
        observation: { content: "Prepare a work brief." },
      },
    },
  ]);
});

test("runObservable posts to the encoded manual-run endpoint", async () => {
  const originalFetch = globalThis.fetch;
  let requestURL = "";
  let requestMethod = "";
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requestURL = String(input);
    requestMethod = String(init?.method);
    return new Response(JSON.stringify({
      id: "observation-1",
      observable_id: "schedule / source",
      source_event_id: "schedule:schedule / source:manual:abc",
      kind: "heartbeat",
      severity: "info",
      window_start: 1,
      window_end: 1,
      content: "manual payload",
      original_chars: 14,
      state: "recorded",
      created_at: 1,
    }), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  try {
    const result = await runObservable("schedule / source");
    assert.equal(result.id, "observation-1");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(requestURL, "/api/observables/schedule%20%2F%20source/run");
  assert.equal(requestMethod, "POST");
});
