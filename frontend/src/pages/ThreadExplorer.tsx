import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Archive, ArchiveRestore, MessageSquareText, Plus, RefreshCw, Trash2 } from "lucide-react";

import { archiveThread, createThread, deleteThread, listThreads, unarchiveThread } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { AgentRuntimeStateBar } from "@/components/fleet/AgentRuntimeStateBar";
import { useFleetAgent } from "@/components/fleet/FleetAgentContext";
import { Button } from "@/components/ui/button";
import { agentPathFromLocation } from "@/lib/fleet-routes";
import { threadHref, threadListTitle } from "@/lib/thread-list";
import { cn } from "@/lib/utils";
import type { ThreadListItem } from "@/types";

export function ThreadExplorer() {
  const navigate = useNavigate();
  const { agent, agentsLoaded } = useFleetAgent();
  const [active, setActive] = useState<ThreadListItem[]>([]);
  const [archived, setArchived] = useState<ThreadListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [mutatingID, setMutatingID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
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
    const answer = window.prompt("Worker Thread alias (optional)");
    if (answer === null) return;
    setCreating(true);
    setError(null);
    try {
      const created = await createThread(answer.trim());
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
            <Button size="sm" onClick={() => void handleCreate()} disabled={creating || !mutationsEnabled}>
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
            <ThreadSection title="Active" empty="No active threads." threads={active} mutatingID={mutatingID} mutationsEnabled={mutationsEnabled} onAction={(thread) => void mutate(thread, "archive")} />
            <ThreadSection title="Archived" empty="No archived threads." threads={archived} archived mutatingID={mutatingID} mutationsEnabled={mutationsEnabled} onAction={(thread) => void mutate(thread, "unarchive")} onDelete={(thread) => void mutate(thread, "delete")} />
          </>
        )}
      </div>
    </div>
  );
}

function ThreadSection({ title, empty, threads, archived = false, mutatingID, mutationsEnabled, onAction, onDelete }: {
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
            {threads.map((thread) => <ThreadRow key={thread.thread_id} thread={thread} archived={archived} busy={mutatingID === thread.thread_id} mutationsEnabled={mutationsEnabled} onAction={() => onAction(thread)} onDelete={onDelete ? () => onDelete(thread) : undefined} />)}
          </div>
        )}
      </div>
    </section>
  );
}

function ThreadRow({ thread, archived, busy, mutationsEnabled, onAction, onDelete }: {
  thread: ThreadListItem;
  archived: boolean;
  busy: boolean;
  mutationsEnabled: boolean;
  onAction: () => void;
  onDelete?: () => void;
}) {
  const main = thread.thread_id === "0";
  const usage = thread.token_usage;
  return (
    <div className="group/thread-row grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 px-3 py-2 hover:bg-muted/60">
      <Link to={threadHref(thread.thread_id)} className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-3 rounded-md px-1 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring/35">
        <span className="flex size-8 items-center justify-center rounded-md bg-primary/10 text-primary"><MessageSquareText className="size-4" /></span>
        <span className="min-w-0">
          <span className="block truncate text-sm font-medium text-foreground">{threadListTitle(thread)}</span>
          <span className="mt-1 flex flex-wrap gap-x-3 gap-y-1 font-mono text-[11px] text-muted-foreground">
            <span>{thread.state}</span><span>{thread.turn_count} turns</span><span>Gen {thread.generation_count}</span><span>{thread.pending_input_count} pending</span><span>{thread.current_context_tokens.toLocaleString()} context</span>
            <span>{(usage.input_tokens ?? 0).toLocaleString()} in · {(usage.cached_input_tokens ?? 0).toLocaleString()} cached · {(usage.output_tokens ?? 0).toLocaleString()} out</span><span>{humanAgo(thread.last_activity_at)}</span>
          </span>
        </span>
      </Link>
      {!main ? (
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="icon-sm" disabled={busy || !mutationsEnabled} onClick={onAction} title={archived ? "Unarchive thread" : "Archive thread"} aria-label={archived ? "Unarchive thread" : "Archive thread"}>
            {archived ? <ArchiveRestore className="size-3.5" /> : <Archive className="size-3.5" />}
          </Button>
          {archived && onDelete ? <Button variant="ghost" size="icon-sm" disabled={busy || !mutationsEnabled} onClick={onDelete} title="Delete thread permanently" aria-label="Delete thread permanently" className="text-muted-foreground hover:text-destructive"><Trash2 className="size-3.5" /></Button> : null}
        </div>
      ) : null}
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
