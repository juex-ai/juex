import { createContext, useContext } from "react";

import type { AgentStatus } from "@/types";

export type FleetAgentContextValue = {
  agent: AgentStatus | null;
  agents: AgentStatus[];
  agentsLoaded: boolean;
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
      lifecycleBusy: false,
      startAgent: async () => {},
    };
  }
  return context;
}
