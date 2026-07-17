import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

const appSource = source("../../frontend/src/App.tsx");
const shellSource = source("../../frontend/src/components/AppShell.tsx");
const fleetSource = source("../../frontend/src/pages/Fleet.tsx");
const logsSource = source("../../frontend/src/pages/AgentLogs.tsx");
const configSource = source("../../frontend/src/pages/AgentConfig.tsx");
const viteSource = source("../../frontend/vite.config.ts");

test("router exposes fleet and selected-agent pages", () => {
  for (const route of [
    'path: "agents/:agentId"',
    'path: "sessions/:id"',
    'path: "history"',
    'path: "runtime"',
    'path: "observables"',
    'path: "observables/:id"',
    'path: "logs"',
    'path: "config"',
  ]) {
    assert.match(appSource, new RegExp(route.replace(/[/:]/g, "\\$&")));
  }
});

test("agent shell includes fleet navigation and agent switching", () => {
  assert.match(shellSource, /aria-label="Switch agent"/);
  assert.match(shellSource, /agentSwitchPath/);
  assert.match(shellSource, /aria-label="Fleet"/);
  assert.match(shellSource, /`\$\{agentBase\}\/observables`/);
  assert.match(shellSource, /`\$\{agentBase\}\/history`/);
  assert.match(shellSource, /`\$\{agentBase\}\/runtime`/);
  assert.match(
    shellSource,
    /<Outlet key=\{agentId\} \/>/,
    "agent switches must remount the selected-agent page",
  );
  assert.equal(
    shellSource.match(/<FileTreePanel\s+key=\{agentId\}/g)?.length,
    2,
    "agent switches must remount both workspace panel variants",
  );
});

test("fleet operations expose roster lifecycle logs and config workflows", () => {
  assert.match(fleetSource, /listAgents/);
  assert.match(fleetSource, /runAgentAction/);
  assert.match(fleetSource, /"start" \| "stop" \| "restart"/);
  assert.match(fleetSource, /View logs/);
  assert.match(fleetSource, /Edit config/);

  assert.match(logsSource, /getAgentLogs\(agentId, lines\)/);
  assert.match(logsSource, /1000/);
  assert.match(configSource, /updateAgentConfig\(agentId, content\)/);
  assert.match(configSource, /Config was not saved:/);
  assert.match(configSource, /Save and restart/);
});

test("vite proxies agent APIs without stealing selected-agent page routes", () => {
  assert.match(viteSource, /"\^\/agents\/\[\^\/\]\+\/api\(\?:\/\|\$\)"/);
  assert.doesNotMatch(viteSource, /^\s*"\/agents":/m);
});
