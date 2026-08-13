import {
  createContext,
  useCallback,
  useContext,
  useSyncExternalStore,
} from "react";

import type {
  AgentResourceName,
  AgentRuntimeStatusSnapshot,
  AgentStatus,
} from "@/types";
import type { AgentViewModelStore } from "@/lib/agent-view-model-store";

export type FleetAgentContextValue = {
  agent: AgentStatus | null;
  agents: AgentStatus[];
  agentsLoaded: boolean;
  statusStore: AgentViewModelStore | null;
  lifecycleBusy: boolean;
  resourceRevision: Record<AgentResourceName, number>;
  startAgent: () => Promise<void>;
};

const FleetAgentContext = createContext<FleetAgentContextValue | null>(null);

export const FleetAgentProvider = FleetAgentContext.Provider;

export function useFleetAgent(): FleetAgentContextValue {
  const context = useContext(FleetAgentContext);
  if (!context) {
    return {
      agent: null,
      agents: [],
      agentsLoaded: false,
      statusStore: null,
      lifecycleBusy: false,
      resourceRevision: { workspace: 0, scratchpad: 0, observables: 0, runtime: 0 },
      startAgent: async () => {},
    };
  }
  return context;
}

export function useAgentSessionStatus(
  agentID: string | undefined,
  sessionID: string | undefined,
): AgentRuntimeStatusSnapshot | undefined {
  const { statusStore } = useFleetAgent();
  const getSnapshot = useCallback(
    () =>
      statusStore && agentID && sessionID
        ? statusStore.status(agentID, sessionID)
        : undefined,
    [agentID, sessionID, statusStore],
  );

  return useSyncExternalStore(
    statusStore?.subscribe ?? emptySubscribe,
    getSnapshot,
    getSnapshot,
  );
}

function emptySubscribe(): () => void {
  return () => {};
}
