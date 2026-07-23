import { strict as assert } from "node:assert";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  MESSAGE_KINDS,
  MESSAGE_GROUP_RENDERER_KEYS,
  messageGroupRendererKey,
} from "../../frontend/src/lib/session-transcript-renderers.ts";

const backendMessageKinds = JSON.parse(
  readFileSync(
    new URL(
      "../../internal/web/testdata/message-kinds.golden.json",
      import.meta.url,
    ),
    "utf8",
  ),
) as string[];

test("frontend message kinds match the backend contract fixture", () => {
  assert.deepEqual(MESSAGE_KINDS, backendMessageKinds);
});

test("every backend message kind resolves through the renderer registry", () => {
  const expectedUserRenderers = {
    mcp_event: "mcp_event",
    observation: "observation",
    hook_event: "hook_event",
    compact: "compact",
    runtime_context: "default",
    model_fallback: "model_fallback",
    system_notice: "system_notice",
  } as const;

  for (const kind of MESSAGE_KINDS) {
    assert.equal(
      messageGroupRendererKey({ kind, role: "user" }),
      expectedUserRenderers[kind],
    );
  }
});

test("automated user renderers retain their role guards", () => {
  for (const kind of [
    "mcp_event",
    "observation",
    "model_fallback",
    "system_notice",
  ] as const) {
    assert.equal(messageGroupRendererKey({ kind, role: "assistant" }), "default");
    assert.equal(messageGroupRendererKey({ kind, role: "system" }), "default");
  }

  assert.equal(
    messageGroupRendererKey({ kind: "hook_event", role: "assistant" }),
    "hook_event",
  );
  assert.equal(
    messageGroupRendererKey({ kind: "compact", role: "assistant" }),
    "compact",
  );
});

test("ordinary and unknown message groups use the default renderer", () => {
  assert.equal(messageGroupRendererKey({ role: "user" }), "default");
  assert.equal(
    messageGroupRendererKey({ kind: "future_event", role: "user" }),
    "default",
  );
});

test("the local pending compact projection has a registered renderer", () => {
  assert.equal(
    messageGroupRendererKey({
      kind: "compact_pending",
      role: "assistant",
    }),
    "compact_pending",
  );
  assert.ok(MESSAGE_GROUP_RENDERER_KEYS.includes("compact_pending"));
});

test("session route delegates message-kind dispatch to the typed registry", () => {
  const routeSource = readFileSync(
    new URL("../../frontend/src/pages/Session.tsx", import.meta.url),
    "utf8",
  );
  const transcriptSource = readFileSync(
    new URL(
      "../../frontend/src/components/session/SessionTranscript.tsx",
      import.meta.url,
    ),
    "utf8",
  );

  assert.doesNotMatch(routeSource, /group\.kind/);
  assert.match(
    transcriptSource,
    /const messageGroupRendererRegistry: Record<[\s\S]*MessageGroupRendererKey,[\s\S]*ComponentType<MessageGroupRendererProps>/,
  );
  assert.match(
    transcriptSource,
    /messageGroupRendererRegistry\[[\s\S]*messageGroupRendererKey\(props\.group\)/,
  );
});
