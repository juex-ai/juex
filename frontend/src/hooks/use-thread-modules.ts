import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { getThreadModules, subscribeThreadModules } from "@/api";
import { agentBasePath, agentIDFromPath } from "@/lib/fleet-routes";
import { startModuleSnapshotSubscription } from "@/lib/module-snapshot-subscription";
import type { ThreadModulesSnapshot } from "@/module-schema";

export function useThreadModules(threadID: string) {
  const location = useLocation();
  const agentScope = agentBasePath(location.pathname);
  const agentID = agentIDFromPath(location.pathname) ?? undefined;
  const scope = `${agentScope}:${threadID}`;
  const [state, setState] = useState<{ scope: string; snapshot?: ThreadModulesSnapshot; error?: string }>({ scope: "" });
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
