import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { MessageSquareText, Plus, RefreshCw, Trash2 } from "lucide-react";

import { createSession, deleteSession, listSessions } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { Button } from "@/components/ui/button";
import {
  historySessionBadges,
  historySessionHref,
  historySessionTitle,
} from "@/lib/history-sessions";
import { cn } from "@/lib/utils";
import { agentPathFromLocation } from "@/lib/fleet-routes";
import type { SessionInfo } from "@/types";

export function History() {
  const navigate = useNavigate();
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [deletingID, setDeletingID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  useShellTitle("History");

  const refreshSessions = useCallback(async (
    { quiet = false }: { quiet?: boolean } = {},
  ) => {
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      const r = await listSessions();
      setSessions(r.sessions);
    } catch (e) {
      console.error("listSessions failed", e);
      setError(e instanceof Error ? e.message : "Failed to load history.");
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void refreshSessions({ quiet: true });
    const onSessionsChanged = () => void refreshSessions({ quiet: true });
    window.addEventListener("juex:sessions-changed", onSessionsChanged);
    return () => {
      window.removeEventListener("juex:sessions-changed", onSessionsChanged);
    };
  }, [refreshSessions]);

  async function handleNewChat() {
    setCreating(true);
    setError(null);
    try {
      const session = await createSession();
      window.dispatchEvent(new Event("juex:sessions-changed"));
      navigate(
        agentPathFromLocation(`/sessions/${encodeURIComponent(session.id)}`),
      );
    } catch (e) {
      console.error("createSession failed", e);
      setError(e instanceof Error ? e.message : "Failed to create chat.");
    } finally {
      setCreating(false);
    }
  }

  async function handleDelete(session: SessionInfo) {
    const label = historySessionTitle(session);
    if (!window.confirm(`Delete "${label}"?`)) return;
    setDeletingID(session.id);
    setError(null);
    try {
      await deleteSession(session.id);
      setSessions((current) => current.filter((s) => s.id !== session.id));
      window.dispatchEvent(new Event("juex:sessions-changed"));
    } catch (e) {
      console.error("deleteSession failed", e);
      setError(e instanceof Error ? e.message : "Failed to delete session.");
    } finally {
      setDeletingID(null);
    }
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-[920px] flex-col gap-4 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <h1 className="text-xl font-semibold text-foreground">History</h1>
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void refreshSessions()}
              disabled={refreshing}
            >
              <RefreshCw
                className={cn("size-3.5", refreshing && "animate-spin")}
              />
              Refresh
            </Button>
            <Button
              type="button"
              size="sm"
              onClick={() => void handleNewChat()}
              disabled={creating}
            >
              <Plus className="size-3.5" />
              New chat
            </Button>
          </div>
        </div>
        {error ? (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        ) : null}
        <div className="overflow-hidden rounded-md border bg-card shadow-[var(--shadow-xs)]">
          {loading ? (
            <div className="px-4 py-6 text-sm text-muted-foreground">
              Loading history...
            </div>
          ) : sessions.length === 0 ? (
            <div className="px-4 py-10 text-center text-sm text-muted-foreground">
              No sessions yet.
            </div>
          ) : (
            <div className="divide-y">
              {sessions.map((session) => (
                <HistoryRow
                  key={session.id}
                  session={session}
                  deleting={deletingID === session.id}
                  onDelete={() => void handleDelete(session)}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function HistoryRow({
  session,
  deleting,
  onDelete,
}: {
  session: SessionInfo;
  deleting: boolean;
  onDelete: () => void;
}) {
  return (
    <div className="group/history-row grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 px-3 py-2 transition-colors hover:bg-muted/60">
      <Link
        to={historySessionHref(session.id)}
        className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-3 rounded-md px-1 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring/35"
      >
        <span className="flex size-8 items-center justify-center rounded-md bg-primary/10 text-primary">
          <MessageSquareText className="size-4" />
        </span>
        <span className="min-w-0">
          <span className="block truncate text-sm font-medium text-foreground">
            {historySessionTitle(session)}
          </span>
          <span className="mt-1 flex min-w-0 flex-wrap items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
            <span>{humanAgo(session.last_active_at)}</span>
            {historySessionBadges(session).map((badge) => (
              <span key={badge} className="rounded border border-current/20 px-1">
                {badge}
              </span>
            ))}
          </span>
        </span>
      </Link>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        title="Delete session"
        aria-label="Delete session"
        disabled={deleting}
        onClick={onDelete}
        className="text-muted-foreground opacity-100 hover:text-destructive sm:opacity-0 sm:group-hover/history-row:opacity-100 sm:focus-visible:opacity-100"
      >
        <Trash2 className="size-3.5" />
      </Button>
    </div>
  );
}

function humanAgo(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "";
  const diff = Date.now() - t;
  const sec = Math.round(diff / 1000);
  if (sec < 60) return "just now";
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d ago`;
  return new Date(iso).toLocaleDateString();
}
