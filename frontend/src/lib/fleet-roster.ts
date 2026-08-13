import type { AgentStatus } from "../types.ts";

export function mergeFleetRoster(
  current: readonly AgentStatus[],
  next: readonly AgentStatus[],
): AgentStatus[] {
  const currentByID = new Map(current.map((agent) => [agent.id, agent]));
  return next.map((agent) => {
    if (agent.activity || agent.runtime_health !== "healthy") return agent;
    const activity = currentByID.get(agent.id)?.activity;
    return activity ? { ...agent, activity } : agent;
  });
}
