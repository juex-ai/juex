import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Archive, ArchiveRestore, Plus, RefreshCw, Trash2 } from "lucide-react";

import { archiveThread, createThread, deleteThread, listThreads, unarchiveThread } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { AgentRuntimeStateBar } from "@/components/fleet/AgentRuntimeStateBar";
import { useFleetAgent } from "@/components/fleet/FleetAgentContext";
import { ThreadUsageSummary } from "@/components/thread/ThreadUsageSummary";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { agentPathFromLocation } from "@/lib/fleet-routes";
import { threadHref, threadListTitle } from "@/lib/thread-list";
import { cn } from "@/lib/utils";
import type { ThreadListItem } from "@/types";

const THREAD_METADATA_BADGE_CLASS =
  "h-[18px] border-border/70 bg-muted/30 px-1.5 font-mono text-[10px] font-normal text-muted-foreground";

type ThreadNavigation = {
  byID: Map<string, ThreadListItem>;
  highlightedID: string | null;
  registerRow: (id: string, element: HTMLDivElement | null) => void;
  locate: (id: string) => void;
};

export function ThreadExplorer() {
  const navigate = useNavigate();
  const { agent, agentsLoaded } = useFleetAgent();
  const [active, setActive] = useState<ThreadListItem[]>([]);
  const [archived, setArchived] = useState<ThreadListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [workerAlias, setWorkerAlias] = useState("");
  const [mutatingID, setMutatingID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [highlightedID, setHighlightedID] = useState<string | null>(null);
  const highlightTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const rows = useRef(new Map<string, HTMLDivElement>());
  const byID = useMemo(() => new Map([...active, ...archived].map((thread) => [thread.thread_id, thread])), [active, archived]);
  const registerRow = useCallback((id: string, element: HTMLDivElement | null) => {
    if (element) rows.current.set(id, element);
    else rows.current.delete(id);
  }, []);
  const locate = useCallback((id: string) => {
    const row = rows.current.get(id);
    if (!row) return;
    if (highlightTimer.current !== null) clearTimeout(highlightTimer.current);
    row.focus({ preventScroll: true });
    row.scrollIntoView({ block: "center", behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
    setHighlightedID(id);
    highlightTimer.current = setTimeout(() => {
      setHighlightedID(null);
      highlightTimer.current = null;
    }, 3000);
  }, []);
  useEffect(() => () => {
    if (highlightTimer.current !== null) clearTimeout(highlightTimer.current);
  }, []);
  const threadNavigation = { byID, highlightedID, registerRow, locate };
  const mutationsEnabled = agentsLoaded && agent?.runtime_health === "healthy";
  useShellTitle("Threads");

  const refreshThreads = useCallback(async ({ quiet = false }: { quiet?: boolean } = {}) => {
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      const response = await listThreads();
      setActive(response.active_threads);
      setArchived(response.archived_threads);
    } catch (cause) {
      console.error("listThreads failed", cause);
      setError(cause instanceof Error ? cause.message : "Failed to load threads.");
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void refreshThreads({ quiet: true });
    const onThreadsChanged = () => void refreshThreads({ quiet: true });
    window.addEventListener("juex:threads-changed", onThreadsChanged);
    return () => window.removeEventListener("juex:threads-changed", onThreadsChanged);
  }, [refreshThreads]);

  async function handleCreate() {
    if (!mutationsEnabled) return;
    setCreating(true);
    setError(null);
    try {
      const created = await createThread(workerAlias.trim());
      setCreateOpen(false);
      setWorkerAlias("");
      window.dispatchEvent(new Event("juex:threads-changed"));
      navigate(agentPathFromLocation(`/threads/${encodeURIComponent(created.id)}`));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to create Worker Thread.");
    } finally {
      setCreating(false);
    }
  }

  async function mutate(thread: ThreadListItem, action: "archive" | "unarchive" | "delete") {
    if (!mutationsEnabled || thread.thread_id === "0") return;
    if (action === "delete" && !window.confirm(`Permanently delete "${threadListTitle(thread)}"?`)) return;
    setMutatingID(thread.thread_id);
    setError(null);
    try {
      if (action === "archive") await archiveThread(thread.thread_id);
      if (action === "unarchive") await unarchiveThread(thread.thread_id);
      if (action === "delete") await deleteThread(thread.thread_id);
      await refreshThreads({ quiet: true });
      window.dispatchEvent(new Event("juex:threads-changed"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `Failed to ${action} thread.`);
    } finally {
      setMutatingID(null);
    }
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-[1040px] flex-col gap-5 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold text-foreground">Threads</h1>
            <p className="mt-1 text-sm text-muted-foreground">Active and archived Agent work streams.</p>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => void refreshThreads()} disabled={refreshing}>
              <RefreshCw className={cn("size-3.5 motion-reduce:animate-none", refreshing && "animate-spin")} /> Refresh
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)} disabled={creating || !mutationsEnabled}>
              <Plus className="size-3.5" /> New Worker
            </Button>
          </div>
        </div>
        {agentsLoaded && agent && !mutationsEnabled ? <AgentRuntimeStateBar /> : null}
        {error ? <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div> : null}
        {loading ? (
          <div className="rounded-md border bg-card px-4 py-8 text-sm text-muted-foreground">Loading threads...</div>
        ) : (
          <>
            <ThreadSection navigation={threadNavigation} title="Active" empty="No active threads." threads={active} mutatingID={mutatingID} mutationsEnabled={mutationsEnabled} onAction={(thread) => void mutate(thread, "archive")} />
            <ThreadSection navigation={threadNavigation} title="Archived" empty="No archived threads." threads={archived} archived mutatingID={mutatingID} mutationsEnabled={mutationsEnabled} onAction={(thread) => void mutate(thread, "unarchive")} onDelete={(thread) => void mutate(thread, "delete")} />
          </>
        )}
      </div>
      <Dialog open={createOpen} onOpenChange={(open) => {
        if (!creating) {
          setCreateOpen(open);
          if (!open) setWorkerAlias("");
        }
      }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New Worker Thread</DialogTitle>
            <DialogDescription>
              Create an idle Worker Thread. Leave the alias empty to use its generated name.
            </DialogDescription>
          </DialogHeader>
          <form className="space-y-4" onSubmit={(event) => {
            event.preventDefault();
            void handleCreate();
          }}>
            <div className="space-y-1.5">
              <label htmlFor="worker-thread-alias" className="text-xs font-medium">Alias (optional)</label>
              <Input
                id="worker-thread-alias"
                value={workerAlias}
                onChange={(event) => setWorkerAlias(event.target.value)}
                autoComplete="off"
                autoFocus
              />
            </div>
            <DialogFooter className="mx-0 mb-0 px-0 pb-0">
              <Button type="button" variant="outline" disabled={creating} onClick={() => setCreateOpen(false)}>Cancel</Button>
              <Button type="submit" disabled={creating || !mutationsEnabled}>{creating ? "Creating..." : "Create Worker"}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ThreadSection({ navigation, title, empty, threads, archived = false, mutatingID, mutationsEnabled, onAction, onDelete }: {
  navigation: ThreadNavigation;
  title: string;
  empty: string;
  threads: ThreadListItem[];
  archived?: boolean;
  mutatingID: string | null;
  mutationsEnabled: boolean;
  onAction: (thread: ThreadListItem) => void;
  onDelete?: (thread: ThreadListItem) => void;
}) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="font-mono text-xs font-semibold uppercase tracking-[0.16em] text-muted-foreground">{title}</h2>
      <div className="overflow-hidden rounded-md border bg-card shadow-[var(--shadow-xs)]">
        {threads.length === 0 ? <div className="px-4 py-8 text-sm text-muted-foreground">{empty}</div> : (
          <div className="divide-y">
            {threads.map((thread) => <ThreadRow navigation={navigation} key={thread.thread_id} thread={thread} archived={archived} busy={mutatingID === thread.thread_id} mutationsEnabled={mutationsEnabled} onAction={() => onAction(thread)} onDelete={onDelete ? () => onDelete(thread) : undefined} />)}
          </div>
        )}
      </div>
    </section>
  );
}

function ThreadRow({ navigation, thread, archived, busy, mutationsEnabled, onAction, onDelete }: {
  navigation: ThreadNavigation;
  thread: ThreadListItem;
  archived: boolean;
  busy: boolean;
  mutationsEnabled: boolean;
  onAction: () => void;
  onDelete?: () => void;
}) {
  const main = thread.thread_id === "0";
  const parentID = thread.parent_thread_id;
  const parent = parentID ? navigation.byID.get(parentID) : undefined;
  const parentTitle = parent ? threadListTitle(parent) : `#${parentID}`;
  const highlighted = navigation.highlightedID === thread.thread_id;
  return (
    <div ref={(element) => navigation.registerRow(thread.thread_id, element)} tabIndex={-1} data-thread-id={thread.thread_id} data-highlighted={highlighted}
      className={cn("group/thread-row grid grid-cols-[minmax(0,1fr)_auto] items-center gap-1.5 px-2 py-1.5 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35", highlighted ? "bg-[var(--juex-gold-400)]/20 ring-1 ring-inset ring-[var(--juex-gold-400)]/60" : "hover:bg-muted/60")}>
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
          <Link to={threadHref(thread.thread_id)} title={threadListTitle(thread)} className="min-w-0 max-w-full truncate rounded-sm text-sm font-medium leading-5 text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/35">
            {threadListTitle(thread)}
          </Link>
          {!main && parentID ? (
            <button type="button" disabled={!parent} onClick={() => navigation.locate(parentID)}
              title={parent ? `Locate parent: ${parentTitle}` : `Parent #${parentID} is unavailable`}
              className="max-w-full truncate rounded border border-border/70 bg-muted/30 px-1 text-[10px] leading-4 text-muted-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/35 disabled:cursor-not-allowed disabled:opacity-60">
              parent → {parentTitle}
            </button>
          ) : null}
        </div>
        <div className="mt-0.5 flex flex-wrap gap-1">
          <Badge variant="outline" className={THREAD_METADATA_BADGE_CLASS}>{humanAgo(thread.last_activity_at)}</Badge>
          <Badge variant="outline" className={THREAD_METADATA_BADGE_CLASS}>{thread.retention_state === "archived" ? "archived" : thread.execution_state}</Badge>
          <Badge variant="outline" className={THREAD_METADATA_BADGE_CLASS}>{thread.turn_count} turns</Badge>
          <Badge variant="outline" className={THREAD_METADATA_BADGE_CLASS}>Gen {thread.generation_count}</Badge>
          <Badge variant="outline" className={THREAD_METADATA_BADGE_CLASS}>{thread.pending_input_count} pending</Badge>
          <Badge variant="outline" className={THREAD_METADATA_BADGE_CLASS}>{thread.current_context_tokens.toLocaleString()} context</Badge>
          <ThreadUsageSummary usage={thread.token_usage} className={THREAD_METADATA_BADGE_CLASS} />
        </div>
      </div>
      <div className="flex items-center gap-1">
        {!main ? (
          <>
            <Button variant="ghost" size="icon-sm" disabled={busy || !mutationsEnabled} onClick={onAction} title={archived ? "Unarchive thread" : "Archive thread"} aria-label={archived ? "Unarchive thread" : "Archive thread"}>
              {archived ? <ArchiveRestore className="size-3.5" /> : <Archive className="size-3.5" />}
            </Button>
            {archived && onDelete ? <Button variant="ghost" size="icon-sm" disabled={busy || !mutationsEnabled} onClick={onDelete} title="Delete thread permanently" aria-label="Delete thread permanently" className="text-muted-foreground hover:text-destructive"><Trash2 className="size-3.5" /></Button> : null}
          </>
        ) : null}
      </div>
    </div>
  );
}

function humanAgo(iso: string): string {
  const time = new Date(iso).getTime();
  if (!Number.isFinite(time)) return "";
  const minutes = Math.max(0, Math.round((Date.now() - time) / 60000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return days < 7 ? `${days}d ago` : new Date(iso).toLocaleDateString();
}
