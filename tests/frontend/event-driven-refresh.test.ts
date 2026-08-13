import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  subscribeAgentResourceEvents,
  subscribeFleetEvents,
} from "../../frontend/src/api.ts";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

const apiSource = source("../../frontend/src/api.ts");
const shellSource = source("../../frontend/src/components/AppShell.tsx");
const fileTreeSource = source(
  "../../frontend/src/components/FileTreePanel.tsx",
);
const fleetSource = source("../../frontend/src/pages/Fleet.tsx");
const runtimeSource = source("../../frontend/src/pages/Runtime.tsx");
const extensionsSource = source("../../frontend/src/pages/Extensions.tsx");
const observablesSource = source("../../frontend/src/pages/Observables.tsx");
const observableDetailSource = source(
  "../../frontend/src/pages/ObservableDetail.tsx",
);

test("live read models use SSE instead of recurring browser timers", () => {
  for (const [name, contents] of [
    ["shell", shellSource],
    ["file tree", fileTreeSource],
    ["fleet", fleetSource],
    ["runtime", runtimeSource],
    ["extensions", extensionsSource],
    ["observables", observablesSource],
    ["observable detail", observableDetailSource],
  ] as const) {
    assert.doesNotMatch(contents, /setInterval\(/, `${name} still uses setInterval`);
    assert.doesNotMatch(
      contents,
      /setTimeout\([^,]+,\s*(?:3_?000|5_?000|10_?000)/,
      `${name} still uses a recurring refresh timeout`,
    );
  }
  assert.match(shellSource, /subscribeFleetEvents/);
  assert.match(shellSource, /subscribeAgentResourceEvents/);
  assert.match(fleetSource, /event\.type === "fleet\.roster"/);
  assert.match(fileTreeSource, /refreshRevision/);
  assert.match(observablesSource, /resourceRevision\.observables/);
  assert.match(observableDetailSource, /resourceRevision\.observables/);
  assert.match(runtimeSource, /resourceRevision\.runtime/);
  assert.match(extensionsSource, /resourceRevision\.runtime/);
});

test("typed EventSource helpers isolate fleet and agent resources", () => {
  assert.match(apiSource, /new EventSource\("\/api\/fleet\/events"\)/);
  assert.match(
    apiSource,
    /new EventSource\(agentAPIPath\("\/api\/resource-events"\)\)/,
  );
  assert.match(apiSource, /parsed\.type === "fleet\.roster"/);
  assert.match(apiSource, /parsed\.type === "fleet\.status"/);
  assert.match(apiSource, /parsed\.type === "agent\.process"/);
  assert.match(apiSource, /parsed\.type === "resource\.changed"/);
});

test("fleet and resource subscriptions parse only their typed events", () => {
  const originalEventSource = globalThis.EventSource;
  const sources: FakeEventSource[] = [];
  class FakeEventSource {
    readonly listeners = new Map<string, Array<(event: Event) => void>>();
    readonly url: string;
    closed = false;

    constructor(url: string) {
      this.url = url;
      sources.push(this);
    }

    addEventListener(type: string, listener: (event: Event) => void) {
      const listeners = this.listeners.get(type) ?? [];
      listeners.push(listener);
      this.listeners.set(type, listeners);
    }

    emit(data: unknown) {
      const event = new MessageEvent("message", { data: JSON.stringify(data) });
      for (const listener of this.listeners.get("message") ?? []) listener(event);
    }

    close() {
      this.closed = true;
    }
  }
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
  const fleetTypes: string[] = [];
  const resources: string[] = [];
  try {
    const closeFleet = subscribeFleetEvents({
      onEvent: (event) => fleetTypes.push(event.type),
    });
    assert.equal(sources[0].url, "/api/fleet/events");
    sources[0].emit({ type: "fleet.roster", agents: [] });
    sources[0].emit({ type: "fleet.status", process: { rss_bytes: 1 } });
    sources[0].emit({
      type: "agent.process",
      agent_id: "one",
      process: { rss_bytes: 2 },
    });
    sources[0].emit({ type: "unknown" });

    const closeResources = subscribeAgentResourceEvents({
      onEvent: (event) => resources.push(...event.resources),
    });
    assert.equal(sources[1].url, "/api/resource-events");
    sources[1].emit({ type: "resource.changed", resources: ["workspace"] });
    sources[1].emit({ type: "resource.changed", resources: "workspace" });

    closeFleet();
    closeResources();
    assert.deepEqual(fleetTypes, [
      "fleet.roster",
      "fleet.status",
      "agent.process",
    ]);
    assert.deepEqual(resources, ["workspace"]);
    assert.equal(sources[0].closed, true);
    assert.equal(sources[1].closed, true);
  } finally {
    globalThis.EventSource = originalEventSource;
  }
});
