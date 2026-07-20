import type {
  AgentActivity,
  AgentRuntimeStatusSnapshot,
  AgentStatus,
  FleetAgentStatusEvent,
} from "../types.ts";

export class AgentViewModelStore {
  private readonly activities = new Map<string, AgentActivity>();
  private readonly sessionStatuses = new Map<
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
    if (this.setSessionStatusInternal(agentID, status)) {
      this.emit();
    }
  }

  clearStatus(agentID: string, sessionID: string): void {
    const statuses = this.sessionStatuses.get(agentID);
    if (!statuses?.delete(sessionID)) return;
    if (statuses.size === 0) this.sessionStatuses.delete(agentID);
    this.emit();
  }

  applyFleetEvent(event: FleetAgentStatusEvent): void {
    if (this.setActivityInternal(event.agent_id, event.activity)) this.emit();
  }

  status(
    agentID: string,
    sessionID: string,
  ): AgentRuntimeStatusSnapshot | undefined {
    return this.sessionStatuses.get(agentID)?.get(sessionID);
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

  private setSessionStatusInternal(
    agentID: string,
    status: AgentRuntimeStatusSnapshot,
  ): boolean {
    let statuses = this.sessionStatuses.get(agentID);
    if (!statuses) {
      statuses = new Map();
      this.sessionStatuses.set(agentID, statuses);
    }
    const current = statuses.get(status.session.id);
    if (sameStatus(current, status)) return false;
    statuses.set(status.session.id, status);
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
