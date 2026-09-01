import assert from "node:assert/strict";
import test from "node:test";

import {
  runtimeSectionFromPath,
  runtimeSectionPath,
  runtimeSections,
} from "../../frontend/src/lib/runtime-navigation.ts";

test("runtime sections expose the canonical selector order and routes", () => {
  assert.deepEqual(runtimeSections, [
    { id: "overview", label: "Overview" },
    { id: "extensions", label: "Extensions" },
    { id: "observables", label: "Observables" },
    { id: "logs", label: "Logs" },
    { id: "config", label: "Config" },
  ]);
  assert.equal(runtimeSectionPath("agent one", "overview"), "/agents/agent%20one/runtime");
  assert.equal(runtimeSectionPath("agent one", "extensions"), "/agents/agent%20one/runtime/extensions");
  assert.equal(runtimeSectionPath("agent one", "observables"), "/agents/agent%20one/runtime/observables");
  assert.equal(runtimeSectionPath("agent one", "logs"), "/agents/agent%20one/runtime/logs");
  assert.equal(runtimeSectionPath("agent one", "config"), "/agents/agent%20one/runtime/config");
});

test("runtime section selection follows nested paths and browser navigation", () => {
  assert.equal(runtimeSectionFromPath("/agents/a/runtime"), "overview");
  assert.equal(runtimeSectionFromPath("/agents/a/runtime/extensions"), "extensions");
  assert.equal(runtimeSectionFromPath("/agents/a/runtime/observables"), "observables");
  assert.equal(runtimeSectionFromPath("/agents/a/runtime/observables/item"), "observables");
  assert.equal(runtimeSectionFromPath("/agents/a/runtime/logs"), "logs");
  assert.equal(runtimeSectionFromPath("/agents/a/runtime/config"), "config");
  assert.equal(runtimeSectionFromPath("/agents/a/threads/one"), "overview");
});
