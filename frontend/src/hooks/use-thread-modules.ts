import { createContext, useContext, useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { getThreadModules, subscribeThreadModules } from "@/api";
import { agentBasePath, agentIDFromPath } from "@/lib/fleet-routes";
import { startModuleSnapshotSubscription } from "@/lib/module-snapshot-subscription";
import type { ThreadModulesSnapshot } from "@/module-schema";

type ThreadModulesState = { scope: string; snapshot?: ThreadModulesSnapshot; error?: string };
const ThreadModulesContext = createContext<ThreadModulesState>({ scope: "" });
export const ThreadModulesProvider = ThreadModulesContext.Provider;

export function useThreadModules(threadID: string): ThreadModulesState {
  const state = useContext(ThreadModulesContext);
  const location = useLocation();
  const scope = `${agentBasePath(location.pathname)}:${threadID}`;
  return state.scope === scope ? state : { scope };
}

export function useThreadModuleSubscription(threadID: string): ThreadModulesState {
  const location = useLocation();
  const agentScope = agentBasePath(location.pathname);
  const agentID = agentIDFromPath(location.pathname) ?? undefined;
  const scope = `${agentScope}:${threadID}`;
  const [state, setState] = useState<ThreadModulesState>({ scope: "" });
  useEffect(() => {
    if (!threadID) return;
    return startModuleSnapshotSubscription({
      threadID,
      agentID,
      load: (signal) => getThreadModules(threadID, signal),
      subscribe: (receive) => subscribeThreadModules(threadID, receive),
      onSnapshot: (snapshot) => setState({ scope, snapshot }),
      onError: (error) => setState({ scope, error: error instanceof Error ? error.message : "Module state unavailable" }),
    });
  }, [threadID, agentID, scope]);
  return state.scope === scope ? state : { scope };
}
