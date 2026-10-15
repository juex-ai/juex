import test from "node:test";
import assert from "node:assert/strict";
import { startModuleSnapshotSubscription } from "../../frontend/src/lib/module-snapshot-subscription.ts";
import type { ThreadModulesSnapshot } from "../../frontend/src/module-schema.ts";

function snapshot(revision: string, agent = "a", thread = "0"): ThreadModulesSnapshot {
  return { agent_id: agent, thread_id: thread, composition_revision: "composition", revision, read_only: false,
    observed_cursor: { generation_id: "1", seq: 1, offset: 1 }, modules: {}, ui: [] };
}

test("stream baseline wins over an old GET and duplicate snapshots are ignored", async () => {
  let finish!: (value: ThreadModulesSnapshot) => void;
  let receive!: (value: ThreadModulesSnapshot) => void;
  const accepted: string[] = [];
  const close = startModuleSnapshotSubscription({ threadID: "0", agentID: "a",
    load: () => new Promise((resolve) => { finish = resolve; }),
    subscribe: (callback) => { receive = callback; return () => {}; },
    onSnapshot: (value) => accepted.push(value.revision), onError: assert.fail,
  });
  receive(snapshot("stream")); receive(snapshot("stream")); finish(snapshot("old-get"));
  await Promise.resolve(); assert.deepEqual(accepted, ["stream"]);
  close(); receive(snapshot("late-old-stream")); assert.deepEqual(accepted, ["stream"]);
});

test("route disposal and scope checks isolate Agent and Thread snapshots", async () => {
  let receive!: (value: ThreadModulesSnapshot) => void;
  let finish!: (value: ThreadModulesSnapshot) => void;
  let signal!: AbortSignal;
  const accepted: string[] = [];
  const close = startModuleSnapshotSubscription({ threadID: "0", agentID: "a",
    load: (s) => { signal = s; return new Promise((resolve) => { finish = resolve; }); },
    subscribe: (callback) => { receive = callback; return () => {}; },
    onSnapshot: (value) => accepted.push(value.revision), onError: assert.fail,
  });
  receive(snapshot("other-agent", "b")); receive(snapshot("other-thread", "a", "worker"));
  receive(snapshot("valid")); close(); finish(snapshot("late-request")); await Promise.resolve();
  assert.equal(signal.aborted, true); assert.deepEqual(accepted, ["valid"]);
});

test("a reconnect replaces the complete composition and clears removed modules", async () => {
  let receive!: (value: ThreadModulesSnapshot) => void;
  const accepted: ThreadModulesSnapshot[] = [];
  const close = startModuleSnapshotSubscription({ threadID: "0",
    load: async () => snapshot("get"),
    subscribe: (callback) => { receive = callback; return () => {}; },
    onSnapshot: (value) => accepted.push(value), onError: assert.fail,
  });
  await Promise.resolve();
  const enabled = snapshot("enabled"); enabled.ui = [{ id: "example.status", module_id: "example", version: 1 }];
  receive(enabled); const disabled = snapshot("disabled"); disabled.composition_revision = "restart"; receive(disabled);
  assert.deepEqual(accepted.at(-1)?.ui, []); assert.deepEqual(accepted.at(-1)?.modules, {}); close();
});

test("stream failures mark the snapshot stale until a matching baseline recovers, even at the same revision", async () => {
  let receive!: (value: ThreadModulesSnapshot) => void;
  let fail!: (error: unknown) => void;
  let finish!: (value: ThreadModulesSnapshot) => void;
  const accepted: string[] = [];
  const failures: unknown[] = [];
  const close = startModuleSnapshotSubscription({ threadID: "0", agentID: "a",
    load: () => new Promise((resolve) => { finish = resolve; }),
    subscribe: (callback, onError) => { receive = callback; fail = onError; return () => {}; },
    onSnapshot: (value) => accepted.push(value.revision), onError: (error) => failures.push(error),
  });
  receive(snapshot("baseline"));
  assert.equal(typeof fail, "function");
  fail("disconnected");
  finish(snapshot("old-get")); await Promise.resolve();
  assert.deepEqual(failures, ["disconnected"]);
  assert.deepEqual(accepted, ["baseline"]);
  receive(snapshot("baseline", "other-agent"));
  receive(snapshot("baseline")); receive(snapshot("baseline"));
  assert.deepEqual(accepted, ["baseline", "baseline"]);
  close(); fail("late-error");
  assert.deepEqual(failures, ["disconnected"]);
});

test("a pending GET cannot clear a stream failure before the first stream baseline", async () => {
  let receive!: (value: ThreadModulesSnapshot) => void;
  let fail!: (error: unknown) => void;
  let finish!: (value: ThreadModulesSnapshot) => void;
  const accepted: string[] = [];
  const failures: unknown[] = [];
  const close = startModuleSnapshotSubscription({ threadID: "0",
    load: () => new Promise((resolve) => { finish = resolve; }),
    subscribe: (callback, onError) => { receive = callback; fail = onError; return () => {}; },
    onSnapshot: (value) => accepted.push(value.revision), onError: (error) => failures.push(error),
  });
  assert.equal(typeof fail, "function");
  fail("unavailable"); finish(snapshot("old-get")); await Promise.resolve();
  assert.deepEqual(failures, ["unavailable"]); assert.deepEqual(accepted, []);
  receive(snapshot("reconnected")); assert.deepEqual(accepted, ["reconnected"]);
  close();
});
