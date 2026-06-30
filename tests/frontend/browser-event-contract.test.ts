import { readFileSync } from "node:fs";
import { strict as assert } from "node:assert";
import test from "node:test";

import { messagesToGroups } from "../../frontend/src/lib/display-units.ts";
import {
  createLiveSessionProjection,
  projectLiveSessionEvent,
} from "../../frontend/src/lib/live-session-projection.ts";
import {
  BROWSER_EVENT_TYPES,
  type BrowserEvent,
} from "../../frontend/src/types.ts";

test("frontend browser event type list matches backend contract fixture", () => {
  assert.deepEqual(BROWSER_EVENT_TYPES, readJSON("browser-event-types.golden.json"));
});

test("frontend projects backend browser event fixture stream", () => {
  const events = readJSON("browser-events.golden.json") as BrowserEvent[];
  let state = createLiveSessionProjection();
  const effects = [];

  for (const event of events) {
    const result = projectLiveSessionEvent(state, event);
    state = result.state;
    effects.push(...result.effects);
  }

  assert.equal(state.tokenUsage?.input_tokens, 10);
  assert.equal(state.contextUsage?.total_tokens, 15);
  assert.equal(state.compactActive, false);

  const groups = messagesToGroups(state.messages);
  const toolUnit = groups
    .flatMap((group) => group.units)
    .find(
      (unit) =>
        unit.kind === "tool" && unit.use?.tool_use_id === "tool-1",
    );
  assert.equal(toolUnit?.kind, "tool");
  if (toolUnit?.kind === "tool") {
    assert.equal(toolUnit.use?.tool_name, "exec_command");
    assert.equal(toolUnit.result?.content, "hi\n");
  }

  assert.ok(
    state.messages.some(
      (message) =>
        message.kind === "hook_event" &&
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text.includes("hook extract-state"),
    ),
  );
  assert.ok(
    state.messages.some(
      (message) =>
        message.role === "user" &&
        message.blocks?.[0]?.type === "text" &&
        message.blocks[0].text === "queued follow-up",
    ),
  );
  assert.deepEqual(effects.at(-1), {
    type: "refresh",
    preserveLiveMessages: true,
  });
});

function readJSON(name: string): unknown {
  return JSON.parse(
    readFileSync(new URL(`../../internal/web/testdata/${name}`, import.meta.url), "utf8"),
  );
}
