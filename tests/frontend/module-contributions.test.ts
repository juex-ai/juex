import assert from "node:assert/strict";
import test from "node:test";
import { resolveContributions } from "../../frontend/src/modules/resolve.ts";
import type { ModuleContribution } from "../../frontend/src/modules/types.ts";
import type { ThreadModulesSnapshot } from "../../frontend/src/module-schema.ts";

const Component = () => null;
const registry: ModuleContribution[] = [
  { id: "test.status", moduleID: "test", version: 1, order: 20, label: "Test", slot: "thread.status", Component },
  { id: "first.status", moduleID: "first", version: 1, order: 10, label: "First", slot: "thread.status", Component },
];
function snapshot(ids: string[]): ThreadModulesSnapshot {
  return { agent_id: "a", thread_id: "0", composition_revision: "c", revision: "r", read_only: false,
    observed_cursor: { generation_id: "g", seq: 0, offset: 0 },
    ui: ids.map((id) => ({ id: `${id}.status`, module_id: id, version: 1 })),
    modules: Object.fromEntries(ids.map((id) => [id, { module_id: id, version: 1, revision: "r", status: "ready", value: null, resources: [], operations: [] }])),
  };
}
test("a new renderer needs only a registration and a server contribution", () => {
  const result = resolveContributions(snapshot(["test", "first"]), registry);
  assert.deepEqual(result.status.map((item) => item.definition.id), ["first.status", "test.status"]);
  assert.equal(result.status[1].definition.Component, Component);
  assert.equal(result.status[1].state.value, null, "enabled empty state still mounts");
  assert.deepEqual(resolveContributions(snapshot([]), registry).status, []);
});
test("unknown, mismatched and unsupported contributions are observable and isolated", () => {
  const data = snapshot(["test", "first", "unknown"]);
  data.ui[0].version = 2;
  const result = resolveContributions(data, registry);
  assert.deepEqual(result.status.map((item) => item.definition.id), ["first.status"]);
  assert.equal(result.diagnostics.length, 2);
  data.ui[0].version = 1;
  data.modules.test.module_id = "first";
  assert.match(resolveContributions(data, registry).diagnostics[0], /test.status/);
});
test("a file root requires its declared resource and ready state", () => {
  const root = { ...registry[0], slot: "file.root", resource: "files", emptyLabel: "Empty", loadTree: async () => ({}), loadContent: async () => ({}), rawURL: () => "", subscribe: () => () => {} } as unknown as ModuleContribution;
  const data = snapshot(["test"]);
  assert.equal(resolveContributions(data, [root]).files.length, 0);
  data.modules.test.resources = ["files"];
  assert.equal(resolveContributions(data, [root]).files.length, 1);
  data.modules.test.status = "error";
  assert.equal(resolveContributions(data, [root]).files.length, 0);
});
