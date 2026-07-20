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
const historySource = source("../../frontend/src/pages/History.tsx");
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
    shellSource.match(/rootKey=\{filePanelKey\}/g)?.length,
    2,
    "root switches must reset both file panel variants",
  );
  assert.match(shellSource, /<FleetEmptyState \/>/);
  assert.match(shellSource, /View logs/);
});

test("fleet rail exposes compact status and exactly two hover actions", () => {
  assert.match(sidebarSource, /data-collapsed=\{compact \? "true" : "false"\}/);
  assert.match(sidebarSource, /aria-label="Expand fleet sidebar"/);
  assert.match(sidebarSource, /aria-label="Collapse fleet sidebar"/);
  assert.match(sidebarSource, /aria-current=\{selected \? "true" : undefined\}/);
  assert.match(sidebarSource, /pointer-events-none/);
  assert.match(sidebarSource, /mobile \? "pointer-events-auto opacity-100"/);
  assert.match(sidebarSource, /group-hover:opacity-100/);
  assert.match(sidebarSource, /nextAgentLifecycleAction/);
  assert.match(sidebarSource, /Open \$\{name\} runtime/);
  assert.match(sidebarSource, /pending inputs/);
  assert.match(sidebarSource, /motion-reduce:animate-none/);
  assert.doesNotMatch(sidebarSource, /before:absolute/);
  assert.doesNotMatch(sidebarSource, /bg-primary\/10" : "hover/);
  assert.equal(
    sidebarSource.match(/className="size-8"/g)?.length,
    2,
    "expanded agent rows should reveal one lifecycle action and one runtime action",
  );
});

test("stage remounts existing pages through tabs and gates offline composers", () => {
  for (const label of ["Chat", "Runtime", "Observables", "Logs", "Config"]) {
    assert.match(stageHeaderSource, new RegExp(`label: "${label}"`));
  }
  assert.match(stageHeaderSource, /agentTabPath\(agent\.id, tab\.id\)/);
  assert.match(stageHeaderSource, /filePanelTitle: string/);
  assert.match(stageHeaderSource, /filePanelActionLabel/);
  assert.match(stateBarSource, /Start agent/);
  assert.match(stateBarSource, /data-testid="agent-runtime-state-bar"/);
  assert.match(sessionSource, /<AgentRuntimeStateBar \/>/);
  assert.match(sessionsSource, /<AgentRuntimeStateBar \/>/);
  assert.match(historySource, /<AgentRuntimeStateBar \/>/);
  assert.match(
    historySource,
    /const mutationsEnabled =\s+agentsLoaded && agent\?\.runtime_health === "healthy"/,
    "history mutations require a loaded healthy agent",
  );
  assert.match(
    historySource,
    /disabled=\{creating \|\| !mutationsEnabled\}/,
    "offline agents must not create sessions from history",
  );
  assert.match(
    historySource,
    /disabled=\{deleting \|\| !mutationsEnabled\}/,
    "offline agents must not delete sessions from history",
  );
  assert.match(
    historySource,
    /!agentsLoaded\s+\? "Loading agent\.\.\."/,
    "history actions must describe the agent loading state accurately",
  );
  assert.match(
    historySource,
    /<HistoryRow[\s\S]*agentsLoaded=\{agentsLoaded\}/,
    "history rows must receive the already-resolved fleet loading state",
  );
  assert.match(
    sessionSource,
    /getSessionStatus\(id\)[\s\S]*subscribeSessionStatus\(id,[\s\S]*statusStore\.setStatus/,
    "session runtime state must restore from a snapshot before subscribing",
  );
  assert.match(
    sessionSource,
    /composerSubmitAction\(\{[\s\S]*status: runtimeStatus/,
    "the composer must derive admission state from the shared runtime status",
  );
  assert.doesNotMatch(sessionSource, /startTurnStatusPolling\(/);
  assert.match(
    sessionSource,
    /!agentsLoaded \|\| agent\?\.runtime_health === "healthy"/,
    "a missing selected agent must not be treated as a healthy runtime",
  );
  assert.match(
    sessionsSource,
    /agentsLoaded && agent && agent\.runtime_health !== "healthy"/,
    "the stopped state bar requires a real selected agent",
  );
  assert.match(
    shellSource,
    /invalidAgentRoute[\s\S]*Loading agent[\s\S]*<Outlet/,
    "invalid agent routes must be redirected before child pages can compose",
  );
  assert.doesNotMatch(
    shellSource,
    /setInterval\(\(\) => void refreshAgents/,
    "roster polling must not overlap slow refresh requests",
  );
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
  assert.match(fleetSource, /sm:items-end/);
  assert.match(fleetSource, /data-selected=\{selected \? "true" : "false"\}/);
  assert.match(fleetSource, /aria-pressed=\{selected\}/);
  assert.match(fleetSource, /className="min-h-0 flex-1 overflow-y-auto pr-1"/);
  assert.match(fleetSource, /<DialogFooter className="shrink-0">/);
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
