import type { AgentStatus } from "../types.ts";

export type AgentVisualState = "stopped" | "idle" | "working" | "failed";
export type AgentStageTab =
  | "chat"
  | "runtime"
  | "observables"
  | "logs"
  | "config";
export type AgentLifecycleAction = "start" | "stop";

export function agentVisualState(agent: AgentStatus): AgentVisualState {
  if (agent.runtime_health === "stopped") return "stopped";
  if (
    agent.runtime_health === "unhealthy" ||
    agent.runtime_health === "ambiguous"
  ) {
    return "failed";
  }
  return agent.activity?.state === "working" ? "working" : "idle";
}

export function agentStatusText(agent: AgentStatus): string {
  const state = agentVisualState(agent);
  if (state === "stopped") return "Stopped";
  if (state === "failed") return agent.problem || "Needs attention";
  if (state === "working") {
    return agent.activity?.session_alias
      ? `Working · ${agent.activity.session_alias}`
      : "Working";
  }
  const workspaceName = lastPathSegment(agent.workspace);
  return workspaceName ? `Idle · ${workspaceName}` : "Idle";
}

export function resolveAgentSelection(
  agents: readonly AgentStatus[],
  storedAgentID: string | null | undefined,
): string | null {
  if (
    storedAgentID &&
    agents.some((candidate) => candidate.id === storedAgentID)
  ) {
    return storedAgentID;
  }
  return agents[0]?.id ?? null;
}

export function agentTabFromPath(pathname: string): AgentStageTab {
  const suffix = pathname.replace(/^\/agents\/[^/]+/, "");
  if (suffix === "/runtime") return "runtime";
  if (suffix.startsWith("/observables")) return "observables";
  if (suffix === "/logs") return "logs";
  if (suffix === "/config") return "config";
  return "chat";
}

export function agentTabPath(
  agentID: string,
  tab: AgentStageTab,
): string {
  const base = `/agents/${encodeURIComponent(agentID)}`;
  switch (tab) {
    case "runtime":
      return `${base}/runtime`;
    case "observables":
      return `${base}/observables`;
    case "logs":
      return `${base}/logs`;
    case "config":
      return `${base}/config`;
    default:
      return base;
  }
}

export function nextAgentLifecycleAction(
  agent: AgentStatus,
): AgentLifecycleAction {
  return agent.runtime_health === "healthy" ? "stop" : "start";
}

function lastPathSegment(path: string | undefined): string {
  if (!path) return "";
  const normalized = path.replace(/[\\/]+$/, "");
  return normalized.split(/[\\/]/).at(-1) ?? "";
}
