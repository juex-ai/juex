import { useMemo, useState } from "react";
import type { ThreadModulesSnapshot } from "@/module-schema";
import { moduleContributions } from "./registry";
import { resolveContributions } from "./resolve";
import type { FileContribution } from "./types";

export function useModuleFilePanel({ agentID, threadID, snapshot, workspaceHealthy, workspaceRevision }: {
  agentID: string; threadID: string; snapshot?: ThreadModulesSnapshot; workspaceHealthy: boolean; workspaceRevision: number;
}) {
  const { files } = useMemo(() => resolveContributions(snapshot, moduleContributions), [snapshot]);
  const scopeKey = JSON.stringify([agentID, threadID, snapshot?.composition_revision]);
  const [selection, setSelection] = useState({ scopeKey, id: "workspace" });
  const selected = selection.scopeKey === scopeKey ? files.find((item) => item.id === selection.id) : undefined;
  // Forget removed roots, including the case where the same composition later returns.
  if (selection.scopeKey !== scopeKey || (selection.id !== "workspace" && !selected)) {
    setSelection({ scopeKey, id: "workspace" });
  }
  const ports = useFilePorts(selected, agentID, threadID);
  const title = selected?.label ?? "Workspace";
  return {
    title,
    rootKey: `${scopeKey}:${selected?.id ?? "workspace"}:${selected ? "module" : workspaceHealthy}`,
    emptyLabel: selected?.emptyLabel ?? "This directory is empty.",
    unavailableReason: !selected && !workspaceHealthy ? "Workspace unavailable while the agent is stopped." : undefined,
    ...ports,
    subscribeChanges: snapshot?.read_only ? undefined : ports.subscribeChanges,
    refreshRevision: selected ? 0 : workspaceRevision,
    refreshLabel: `Refresh ${title.toLowerCase()}`,
    headerAction: files.length ? <select aria-label="File root" value={selected?.id ?? "workspace"}
      className="h-7 min-w-0 max-w-28 rounded-sm border bg-background px-1 text-xs font-normal normal-case tracking-normal text-foreground"
      onChange={(event) => setSelection({ scopeKey, id: event.target.value })}>
      <option value="workspace">Workspace</option>
      {files.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
    </select> : undefined,
  };
}

function useFilePorts(root: FileContribution | undefined, agentID: string, threadID: string) {
  return useMemo(() => {
    if (!root) return {};
    const scope = { agentID, threadID };
    return {
      loadTree: (signal?: AbortSignal) => root.loadTree(scope, signal),
      loadContent: (path: string, signal?: AbortSignal) => root.loadContent(scope, path, signal),
      rawURL: (path: string) => root.rawURL(scope, path),
      subscribeChanges: (receive: () => void) => root.subscribe(scope, receive),
    };
  }, [root, agentID, threadID]);
}
