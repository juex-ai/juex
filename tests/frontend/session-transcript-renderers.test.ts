import { strict as assert } from "node:assert";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  MESSAGE_GROUP_RENDERER_KEYS,
  messageGroupRendererKey,
} from "../../frontend/src/lib/session-transcript-renderers.ts";

test("every declared message group renderer kind resolves to itself", () => {
  for (const kind of MESSAGE_GROUP_RENDERER_KEYS) {
    assert.equal(
      messageGroupRendererKey({ kind: kind === "default" ? undefined : kind }),
      kind,
    );
  }
});

test("ordinary and unknown message groups use the default renderer", () => {
  assert.equal(messageGroupRendererKey({}), "default");
  assert.equal(messageGroupRendererKey({ kind: "future_event" }), "default");
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
