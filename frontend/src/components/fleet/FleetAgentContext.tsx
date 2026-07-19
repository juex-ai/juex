import { createContext, useContext } from "react";

import type { AgentStatus } from "@/types";
import type { AgentViewModelStore } from "@/lib/agent-view-model-store";

export type FleetAgentContextValue = {
  agent: AgentStatus | null;
  agents: AgentStatus[];
  agentsLoaded: boolean;
  statusStore: AgentViewModelStore | null;
  lifecycleBusy: boolean;
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
      startAgent: async () => {},
    };
  }
  return context;
}
