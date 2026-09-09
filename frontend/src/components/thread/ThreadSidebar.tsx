import { useEffect, useState, type ComponentProps } from "react";
import { Tabs } from "radix-ui";
import { PanelRightClose } from "lucide-react";
import { getThreadRecitation } from "@/api";
import { FileTreePanel } from "@/components/FileTreePanel";
import { useAgentThreadStatus } from "@/components/fleet/FleetAgentContext";
import { Button } from "@/components/ui/button";
import type { RecitationSnapshot } from "@/types";
import { InspectorSection } from "./InspectorSection";
import { ThreadStatusPanel } from "./ThreadStatusPanel";

export function ThreadSidebar({ agentID, threadID, filePanel, onClose }: {
  agentID: string; threadID: string; filePanel: ComponentProps<typeof FileTreePanel>; onClose: () => void;
}) {
  const [tab, setTab] = useState(threadID ? "status" : "files");
  return <Tabs.Root value={tab} onValueChange={setTab} className="flex h-full min-h-0 min-w-0 flex-col">
    <div className="flex min-h-14 shrink-0 items-center gap-2 border-b px-3">
      <Tabs.List aria-label="Sidebar views" className="flex min-w-0 flex-1 gap-1">
        {threadID ? <SidebarTab value="status">Status</SidebarTab> : null}
        <SidebarTab value="files">Files</SidebarTab>
      </Tabs.List>
      <Button size="icon" variant="ghost" className="size-11 shrink-0" aria-label="Close sidebar" onClick={onClose}>
        <PanelRightClose className="size-4" />
      </Button>
    </div>
    {threadID ? <Tabs.Content value="status" className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-[env(safe-area-inset-bottom)] outline-none">
      <ThreadState agentID={agentID} threadID={threadID} />
    </Tabs.Content> : null}
    <Tabs.Content value="files" forceMount hidden={tab !== "files"} className="min-h-0 flex-1 overflow-hidden data-[state=inactive]:hidden">
      <FileTreePanel key={filePanel.rootKey} {...filePanel} active={tab === "files"} />
    </Tabs.Content>
  </Tabs.Root>;
}

function SidebarTab({ value, children }: { value: string; children: string }) {
  return <Tabs.Trigger value={value} className="min-h-11 rounded-sm px-3 text-xs font-medium text-muted-foreground outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/35 data-[state=active]:bg-muted data-[state=active]:text-foreground">{children}</Tabs.Trigger>;
}

function ThreadState({ agentID, threadID }: { agentID: string; threadID: string }) {
  const status = useAgentThreadStatus(agentID, threadID);
  return <div role="group" aria-label="Thread status" className="min-w-0">
    <ThreadStatusPanel threadID={threadID} runtimeStatus={status} />
    <RecitationSection key={`${agentID}:${threadID}`} agentID={agentID} threadID={threadID}
      working={status?.thread.working ?? false}
      revision={`${status?.turn?.id ?? ""}:${status?.turn?.state ?? ""}:${status?.turn?.phase ?? ""}`} />
  </div>;
}

function RecitationSection({ agentID, threadID, working, revision }: {
  agentID: string; threadID: string; working: boolean; revision: string;
}) {
  const [snapshot, setSnapshot] = useState<RecitationSnapshot | null>();
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const read = async () => {
      try {
        const next = await getThreadRecitation(agentID, threadID, controller.signal);
        if (controller.signal.aborted) return;
        setSnapshot(next);
        setError("");
      } catch (cause) {
        if (controller.signal.aborted) return;
        setError(cause instanceof Error ? cause.message : "Recitation unavailable");
      }
      if (working && !controller.signal.aborted) timer = setTimeout(() => void read(), 3000);
    };
    void read();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [agentID, threadID, working, revision, refresh]);

  const summary = error ? "Unavailable" : snapshot === undefined ? "Loading…" : snapshot === null ? "No recorded request" : `${snapshot.fragments.length} fragments · latest request`;
  return <InspectorSection title="Recitation" summary={summary}>
    <p className="text-muted-foreground">Recorded when the latest request was prepared. Current state may have changed since then.</p>
    {error ? <p role="status" className="text-destructive">{error}{snapshot ? " Showing the last received snapshot." : ""}</p> : null}
    {snapshot === null && !error ? <p>No request has been recorded for this Thread.</p> : null}
    {snapshot ? <>
      <time dateTime={snapshot.recorded_at} className="block text-[11px] text-muted-foreground">{new Date(snapshot.recorded_at).toLocaleString()}</time>
      <div className="text-[11px] text-muted-foreground">{snapshot.generation_id} · Step {snapshot.iter + 1}</div>
      {snapshot.fragments.length === 0 ? <p>This request contains no Recitation fragments.</p> : snapshot.fragments.map((fragment) => <details key={fragment.message_id} className="min-w-0 rounded-sm border border-border/70">
        <summary className="min-h-11 cursor-pointer px-3 py-2.5 text-xs [overflow-wrap:anywhere]">{fragment.text.match(/^#{1,6}\s+(.+)$/m)?.[1] ?? fragment.message_id}</summary>
        <div className="space-y-2 px-3 pb-3">
          <div className="font-mono text-[10px] text-muted-foreground [overflow-wrap:anywhere]">{fragment.message_id}</div>
          <pre className="max-h-80 overflow-y-auto whitespace-pre-wrap font-mono text-[11px] [overflow-wrap:anywhere]">{fragment.text}</pre>
        </div>
      </details>)}
    </> : null}
    <Button variant="outline" size="sm" className="min-h-11" onClick={() => setRefresh((value) => value + 1)}>Refresh Recitation</Button>
  </InspectorSection>;
}
