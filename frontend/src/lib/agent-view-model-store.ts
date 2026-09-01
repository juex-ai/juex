import type {
  AgentActivity,
  AgentRuntimeStatusSnapshot,
  AgentStatus,
  FleetAgentStatusEvent,
} from "../types.ts";

export class AgentViewModelStore {
  private readonly activities = new Map<string, AgentActivity>();
  private readonly threadStatuses = new Map<
    string,
    Map<string, AgentRuntimeStatusSnapshot>
  >();
  private readonly listeners = new Set<() => void>();
  private revision = 0;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getRevision = (): number => this.revision;

  seedAgents(agents: readonly AgentStatus[]): void {
    let changed = false;
    const rosterIDs = new Set(agents.map((agent) => agent.id));
    for (const agentID of this.activities.keys()) {
      if (!rosterIDs.has(agentID)) {
        changed = this.activities.delete(agentID) || changed;
      }
    }
    for (const agentID of this.threadStatuses.keys()) {
      if (!rosterIDs.has(agentID)) {
        changed = this.threadStatuses.delete(agentID) || changed;
      }
    }
    for (const agent of agents) {
      if (agent.runtime_health !== "healthy") {
        changed = this.activities.delete(agent.id) || changed;
        continue;
      }
      if (agent.activity) {
        changed = this.setActivityInternal(agent.id, agent.activity) || changed;
      }
    }
    if (changed) this.emit();
  }

  setStatus(agentID: string, status: AgentRuntimeStatusSnapshot): void {
    if (this.setThreadStatusInternal(agentID, status)) {
      this.emit();
    }
  }

  clearStatus(agentID: string, threadID: string): void {
    const statuses = this.threadStatuses.get(agentID);
    if (!statuses?.delete(threadID)) return;
    if (statuses.size === 0) this.threadStatuses.delete(agentID);
    this.emit();
  }

  applyFleetEvent(event: FleetAgentStatusEvent): void {
    if (this.setActivityInternal(event.agent_id, event.activity)) this.emit();
  }

  status(
    agentID: string,
    threadID: string,
  ): AgentRuntimeStatusSnapshot | undefined {
    return this.threadStatuses.get(agentID)?.get(threadID);
  }

  projectAgents(agents: readonly AgentStatus[]): AgentStatus[] {
    return agents.map((agent) => {
      const activity = this.activities.get(agent.id);
      return activity ? { ...agent, activity } : agent;
    });
  }

  private setActivityInternal(
    agentID: string,
    activity: AgentActivity,
  ): boolean {
    const current = this.activities.get(agentID);
    if (sameActivity(current, activity)) {
      return false;
    }
    this.activities.set(agentID, activity);
    return true;
  }

  private setThreadStatusInternal(
    agentID: string,
    status: AgentRuntimeStatusSnapshot,
  ): boolean {
    let statuses = this.threadStatuses.get(agentID);
    if (!statuses) {
      statuses = new Map();
      this.threadStatuses.set(agentID, statuses);
    }
    const current = statuses.get(status.thread.id);
    if (sameStatus(current, status)) return false;
    statuses.set(status.thread.id, status);
    return true;
  }

  private emit(): void {
    this.revision += 1;
    for (const listener of this.listeners) listener();
  }
}

function sameActivity(
  current: AgentActivity | undefined,
  next: AgentActivity,
): boolean {
  return (
    current?.state === next.state &&
    current?.pending_input_count === next.pending_input_count &&
    sameStatus(current?.selected_status, next.selected_status)
  );
}

function sameStatus(
  current: AgentRuntimeStatusSnapshot | undefined,
  next: AgentRuntimeStatusSnapshot | undefined,
): boolean {
  if (current === next) return true;
  if (!current || !next) return false;
  return JSON.stringify(current) === JSON.stringify(next);
}
