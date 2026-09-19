import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
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
import { threadBatchGroups, threadHref, threadListTitle } from "@/lib/thread-list";
import { aggregateThreadUsage } from "@/lib/thread-usage";
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

type ThreadSelection = { active: Set<string>; archived: Set<string> };
type BatchAction = "archive" | "delete";

export function ThreadExplorer() {
  const { agentId } = useParams<{ agentId: string }>();
  return <AgentThreadExplorer key={agentId} />;
}

function AgentThreadExplorer() {
  const navigate = useNavigate();
  const { agent, agentsLoaded } = useFleetAgent();
  const [active, setActive] = useState<ThreadListItem[]>([]);
  const [archived, setArchived] = useState<ThreadListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [hasSnapshot, setHasSnapshot] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [workerAlias, setWorkerAlias] = useState("");
  const [mutatingID, setMutatingID] = useState<string | null>(null);
  const [batchAction, setBatchAction] = useState<BatchAction | null>(null);
  const [selection, setSelection] = useState<ThreadSelection>(() => ({ active: new Set(), archived: new Set() }));
  const [batchFailures, setBatchFailures] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [highlightedID, setHighlightedID] = useState<string | null>(null);
  const highlightTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const rows = useRef(new Map<string, HTMLDivElement>());
  const listRequest = useRef(0);
  const byID = useMemo(() => new Map([...active, ...archived].map((thread) => [thread.thread_id, thread])), [active, archived]);
  const totalUsage = useMemo(() => aggregateThreadUsage([...byID.values()].map((thread) => thread.token_usage)), [byID]);
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
  const mutationBusy = mutatingID !== null || batchAction !== null;
  useShellTitle("Threads");

  const refreshThreads = useCallback(async ({ quiet = false }: { quiet?: boolean } = {}) => {
    const request = ++listRequest.current;
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      const response = await listThreads();
      if (request !== listRequest.current) return;
      setActive(response.active_threads);
      setArchived(response.archived_threads);
      setHasSnapshot(true);
      setSelection((previous) => ({
        active: new Set(response.active_threads.filter((thread) => thread.thread_id !== "0" && previous.active.has(thread.thread_id)).map((thread) => thread.thread_id)),
        archived: new Set(response.archived_threads.filter((thread) => thread.thread_id !== "0" && previous.archived.has(thread.thread_id)).map((thread) => thread.thread_id)),
      }));
    } catch (cause) {
      if (request !== listRequest.current) return;
      console.error("listThreads failed", cause);
      setError(cause instanceof Error ? cause.message : "Failed to load threads.");
    } finally {
      if (request === listRequest.current) {
        setLoading(false);
        setRefreshing(false);
      }
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
    if (!mutationsEnabled || mutationBusy || thread.thread_id === "0") return;
    if (action === "delete" && !window.confirm(`Permanently delete "${threadListTitle(thread)}"?`)) return;
    setMutatingID(thread.thread_id);
    setError(null);
    setBatchFailures([]);
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

  function selectThreads(section: keyof ThreadSelection, ids: string[], checked: boolean) {
    if (!mutationsEnabled || mutationBusy) return;
    const eligible = new Set((section === "active" ? active : archived).filter((thread) => thread.thread_id !== "0").map((thread) => thread.thread_id));
    setSelection((previous) => {
      const next = new Set(previous[section]);
      for (const id of ids) {
        if (checked && eligible.has(id)) next.add(id);
        else next.delete(id);
      }
      return { ...previous, [section]: next };
    });
  }

  async function mutateSelected(action: BatchAction) {
    if (!mutationsEnabled || mutationBusy || !agent?.id) return;
    const targetAgentID = agent.id;
    const section = action === "archive" ? "active" : "archived";
    const targets = (section === "active" ? active : archived).filter((thread) => thread.thread_id !== "0" && selection[section].has(thread.thread_id));
    if (targets.length === 0) return;
    if (action === "delete" && !window.confirm(`Permanently delete ${targets.length} Threads? This cannot be undone.\n\n${targets.map(threadListTitle).join("\n")}`)) return;
    setBatchAction(action);
    setError(null);
    setBatchFailures([]);
    try {
      const results = new Map<string, PromiseSettledResult<void>>();
      // Store lifecycle rules require selected descendants to settle before their ancestors.
      for (const group of threadBatchGroups(targets, byID)) {
        const settled = await Promise.allSettled(group.map(async (thread) => {
          if (action === "archive") await archiveThread(thread.thread_id, targetAgentID);
          else await deleteThread(thread.thread_id, targetAgentID);
        }));
        settled.forEach((result, index) => results.set(group[index].thread_id, result));
      }
      const succeeded = new Set(targets.filter((thread) => results.get(thread.thread_id)?.status === "fulfilled").map((thread) => thread.thread_id));
      setSelection((previous) => ({ ...previous, [section]: new Set([...previous[section]].filter((id) => !succeeded.has(id))) }));
      setBatchFailures(targets.flatMap((thread) => {
        const result = results.get(thread.thread_id);
        return result?.status === "rejected" ? [`${threadListTitle(thread)}: ${result.reason instanceof Error ? result.reason.message : `Failed to ${action} Thread.`}`] : [];
      }));
      await refreshThreads({ quiet: true });
      window.dispatchEvent(new Event("juex:threads-changed"));
    } finally {
      setBatchAction(null);
    }
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-[1040px] flex-col gap-5 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="flex flex-wrap items-center gap-3">
              <h1 className="text-xl font-semibold text-foreground">Threads</h1>
              {hasSnapshot ? (
                <div role="group" aria-label="Total token usage" className="flex items-center gap-1.5">
                  <span className="text-xs text-muted-foreground">Total</span>
                  <ThreadUsageSummary usage={totalUsage} />
                </div>
              ) : null}
            </div>
            <p className="mt-1 text-sm text-muted-foreground">Active and archived Agent work streams.</p>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => void refreshThreads()} disabled={refreshing || mutationBusy}>
              <RefreshCw className={cn("size-3.5 motion-reduce:animate-none", refreshing && "animate-spin")} /> Refresh
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)} disabled={creating || mutationBusy || !mutationsEnabled}>
              <Plus className="size-3.5" /> New Worker
            </Button>
          </div>
        </div>
        {agentsLoaded && agent && !mutationsEnabled ? <AgentRuntimeStateBar /> : null}
        {error ? <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div> : null}
        {batchFailures.length > 0 ? (
          <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            <p>Some Threads could not be updated. Failed selections are kept for retry.</p>
            <ul className="mt-1 list-inside list-disc break-words">{batchFailures.map((failure) => <li key={failure}>{failure}</li>)}</ul>
          </div>
        ) : null}
        {loading ? (
          <div className="rounded-md border bg-card px-4 py-8 text-sm text-muted-foreground">Loading threads...</div>
        ) : (
          <>
            <ThreadSection navigation={threadNavigation} title="Active" empty="No active threads." threads={active} mutatingID={mutatingID} mutationsEnabled={mutationsEnabled && !mutationBusy} selection={selection.active} onSelection={(ids, checked) => selectThreads("active", ids, checked)} onBatch={() => void mutateSelected("archive")} batchBusy={batchAction === "archive"} onAction={(thread) => void mutate(thread, "archive")} />
            <ThreadSection navigation={threadNavigation} title="Archived" empty="No archived threads." threads={archived} archived mutatingID={mutatingID} mutationsEnabled={mutationsEnabled && !mutationBusy} selection={selection.archived} onSelection={(ids, checked) => selectThreads("archived", ids, checked)} onBatch={() => void mutateSelected("delete")} batchBusy={batchAction === "delete"} onAction={(thread) => void mutate(thread, "unarchive")} onDelete={(thread) => void mutate(thread, "delete")} />
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

function ThreadSection({ navigation, title, empty, threads, archived = false, mutatingID, mutationsEnabled, selection, onSelection, onBatch, batchBusy, onAction, onDelete }: {
  navigation: ThreadNavigation;
  title: string;
  empty: string;
  threads: ThreadListItem[];
  archived?: boolean;
  mutatingID: string | null;
  mutationsEnabled: boolean;
  selection: Set<string>;
  onSelection: (ids: string[], checked: boolean) => void;
  onBatch: () => void;
  batchBusy: boolean;
  onAction: (thread: ThreadListItem) => void;
  onDelete?: (thread: ThreadListItem) => void;
}) {
  const selectable = threads.filter((thread) => thread.thread_id !== "0").map((thread) => thread.thread_id);
  const selectedCount = selectable.filter((id) => selection.has(id)).length;
  return (
    <section aria-label={`${title} threads`} className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <ThreadCheckbox label={`Select all ${title.toLowerCase()} Worker Threads`} checked={selectable.length > 0 && selectedCount === selectable.length} mixed={selectedCount > 0 && selectedCount < selectable.length} disabled={!mutationsEnabled || selectable.length === 0} onChange={(checked) => onSelection(selectable, checked)} />
        <h2 className="font-mono text-xs font-semibold uppercase tracking-[0.16em] text-muted-foreground">{title}</h2>
        {selectedCount > 0 ? (
          <>
            <span className="text-xs text-muted-foreground">{selectedCount} selected</span>
            <Button variant="outline" size="sm" onClick={onBatch} disabled={!mutationsEnabled}>
              {batchBusy ? (archived ? "Deleting..." : "Archiving...") : (archived ? "Delete selected" : "Archive selected")}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => onSelection(selectable, false)} disabled={!mutationsEnabled}>Clear selection</Button>
          </>
        ) : null}
      </div>
      <div className="overflow-hidden rounded-md border bg-card shadow-[var(--shadow-xs)]">
        {threads.length === 0 ? <div className="px-4 py-8 text-sm text-muted-foreground">{empty}</div> : (
          <div className="divide-y">
            {threads.map((thread) => <ThreadRow navigation={navigation} key={thread.thread_id} thread={thread} archived={archived} busy={mutatingID === thread.thread_id} mutationsEnabled={mutationsEnabled} selected={selection.has(thread.thread_id)} onSelect={(checked) => onSelection([thread.thread_id], checked)} onAction={() => onAction(thread)} onDelete={onDelete ? () => onDelete(thread) : undefined} />)}
          </div>
        )}
      </div>
    </section>
  );
}

function ThreadRow({ navigation, thread, archived, busy, mutationsEnabled, selected, onSelect, onAction, onDelete }: {
  navigation: ThreadNavigation;
  thread: ThreadListItem;
  archived: boolean;
  busy: boolean;
  mutationsEnabled: boolean;
  selected: boolean;
  onSelect: (checked: boolean) => void;
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
      className={cn("group/thread-row grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-1.5 px-2 py-1.5 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35", highlighted ? "bg-[var(--juex-gold-400)]/20 ring-1 ring-inset ring-[var(--juex-gold-400)]/60" : "hover:bg-muted/60")}>
      <ThreadCheckbox label={`Select ${threadListTitle(thread)}`} checked={!main && selected} disabled={main || busy || !mutationsEnabled} onChange={onSelect} />
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

function ThreadCheckbox({ label, checked, mixed = false, disabled, onChange }: {
  label: string;
  checked: boolean;
  mixed?: boolean;
  disabled: boolean;
  onChange: (checked: boolean) => void;
}) {
  return <input type="checkbox" aria-label={label} aria-checked={mixed ? "mixed" : checked} checked={checked} disabled={disabled} ref={(element) => { if (element) element.indeterminate = mixed; }} onChange={(event) => onChange(event.target.checked)} className="size-4 shrink-0 cursor-pointer accent-primary outline-none focus-visible:ring-2 focus-visible:ring-ring/35 disabled:cursor-not-allowed disabled:opacity-40" />;
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
