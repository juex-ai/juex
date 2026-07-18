import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

const appSource = source("../../frontend/src/App.tsx");
const shellSource = source("../../frontend/src/components/AppShell.tsx");
const sidebarSource = source(
  "../../frontend/src/components/fleet/FleetSidebar.tsx",
);
const stageHeaderSource = source(
  "../../frontend/src/components/fleet/FleetStageHeader.tsx",
);
const stateBarSource = source(
  "../../frontend/src/components/fleet/AgentRuntimeStateBar.tsx",
);
const sessionSource = source("../../frontend/src/pages/Session.tsx");
const sessionsSource = source("../../frontend/src/pages/Sessions.tsx");
const fleetSource = source("../../frontend/src/pages/Fleet.tsx");
const switchSource = source("../../frontend/src/components/ui/switch.tsx");
const apiSource = source("../../frontend/src/api.ts");
const typesSource = source("../../frontend/src/types.ts");
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
    'path: "settings"',
  ]) {
    assert.match(appSource, new RegExp(route.replace(/[/:]/g, "\\$&")));
  }
});

test("agent shell keeps the fleet rail mounted around selected-agent pages", () => {
  assert.match(appSource, /path: "\/"[\s\S]*element: <AppShell \/>/);
  assert.match(shellSource, /<FleetSidebar/);
  assert.match(shellSource, /<FleetStageHeader/);
  assert.match(shellSource, /resolveAgentSelection/);
  assert.match(shellSource, /juex:fleet:last-agent/);
  assert.match(shellSource, /juex:fleet:sidebar-collapsed/);
  assert.match(shellSource, /MOBILE_SIDEBAR_QUERY = "\(max-width: 759px\)"/);
  assert.match(
    shellSource,
    /<Outlet key=\{agentId \|\| "fleet-settings"\} \/>/,
    "agent switches must remount the selected-agent page",
  );
  assert.equal(
    shellSource.match(/<FileTreePanel\s+key=\{agentId\}/g)?.length,
    2,
    "agent switches must remount both workspace panel variants",
  );
  assert.match(shellSource, /<FleetEmptyState \/>/);
  assert.match(shellSource, /View logs/);
});

test("fleet rail exposes compact status and exactly two hover actions", () => {
  assert.match(sidebarSource, /data-collapsed=\{compact \? "true" : "false"\}/);
  assert.match(sidebarSource, /aria-label="Expand fleet sidebar"/);
  assert.match(sidebarSource, /aria-label="Collapse fleet sidebar"/);
  assert.match(sidebarSource, /group-hover:opacity-100/);
  assert.match(sidebarSource, /nextAgentLifecycleAction/);
  assert.match(sidebarSource, /Open \$\{name\} runtime/);
  assert.match(sidebarSource, /pending inputs/);
  assert.match(sidebarSource, /motion-reduce:animate-none/);
  assert.equal(
    sidebarSource.match(/className="size-8 bg-card\/90"/g)?.length,
    2,
    "expanded agent rows should reveal one lifecycle action and one runtime action",
  );
});

test("stage remounts existing pages through tabs and gates offline composers", () => {
  for (const label of ["Chat", "Runtime", "Observables", "Logs", "Config"]) {
    assert.match(stageHeaderSource, new RegExp(`label: "${label}"`));
  }
  assert.match(stageHeaderSource, /agentTabPath\(agent\.id, tab\.id\)/);
  assert.match(stateBarSource, /Start agent/);
  assert.match(stateBarSource, /data-testid="agent-runtime-state-bar"/);
  assert.match(sessionSource, /<AgentRuntimeStateBar \/>/);
  assert.match(sessionsSource, /<AgentRuntimeStateBar \/>/);
});

test("fleet operations expose roster lifecycle logs and config workflows", () => {
  assert.match(fleetSource, /listAgents/);
  assert.match(fleetSource, /runAgentAction/);
  assert.match(fleetSource, /"start" \| "stop" \| "restart"/);
  assert.match(fleetSource, /View logs/);
  assert.match(fleetSource, /Edit config/);
  for (const operation of [
    "addAgent",
    "listDirectories",
    "setAgentEnabled",
    "removeAgent",
  ]) {
    assert.match(fleetSource, new RegExp(operation));
    assert.match(apiSource, new RegExp(`function ${operation}`));
  }
  assert.match(fleetSource, /Add agent/);
  assert.match(fleetSource, /Show hidden/);
  assert.match(
    fleetSource,
    /const confirmationTarget = agent\.name \|\| agent\.id/,
  );
  assert.match(
    fleetSource,
    /autostart: autostart \? true : undefined/,
    "an untouched false toggle must preserve existing autostart metadata",
  );
  assert.match(fleetSource, /localeCompare\(b\.id\)/);
  assert.match(fleetSource, /agent\.enabled/);
  assert.match(typesSource, /export interface DirectoryListing/);
  assert.match(typesSource, /export interface RemovedAgent/);
  assert.match(switchSource, /data-\[state=checked\]:bg-primary/);
  assert.match(switchSource, /data-\[state=checked\]:translate-x-4/);
  assert.match(switchSource, /data-\[state=unchecked\]:translate-x-0/);
  assert.doesNotMatch(switchSource, /data-(?:checked|unchecked):/);
  assert.match(
    fleetSource,
    /await refresh\(\{ quiet: true \}\);\s+setError\(actionError\)/,
    "roster recovery must not clear the lifecycle action error",
  );

  assert.match(logsSource, /getAgentLogs\(agentId, lines\)/);
  assert.match(logsSource, /1000/);
  assert.match(
    configSource,
    /updateAgentConfig\(agentId, submittedContent\)/,
  );
  assert.match(configSource, /persistedConfig = await getAgentConfig\(agentId\)/);
  assert.match(configSource, /resolveConfigSaveFailure/);
  assert.match(configSource, /Save and restart/);
});

test("vite proxies agent APIs without stealing selected-agent page routes", () => {
  assert.match(viteSource, /"\^\/agents\/\[\^\/\]\+\/api\(\?:\/\|\$\)"/);
  assert.doesNotMatch(viteSource, /^\s*"\/agents":/m);
});
