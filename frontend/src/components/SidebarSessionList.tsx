import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Trash2 } from "lucide-react";
import { deleteSession, listSessions } from "@/api";
import { Button } from "@/components/ui/button";
import type { SessionInfo } from "@/types";
import { cn } from "@/lib/utils";

export function SidebarSessionList({
  createdSession,
}: {
  createdSession: SessionInfo | null;
}) {
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [deletingID, setDeletingID] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const navigate = useNavigate();
  const params = useParams<{ id?: string }>();

  async function handleDelete(id: string) {
    const session = sessions.find((s) => s.id === id);
    const label = session?.preview || id;
    if (!window.confirm(`Delete "${label}"?`)) return;
    setDeletingID(id);
    try {
      await deleteSession(id);
      setSessions((current) => current.filter((s) => s.id !== id));
      if (params.id === id) {
        navigate("/");
      }
    } catch (e) {
      console.error("deleteSession failed", e);
    } finally {
      setDeletingID(null);
    }
  }

  useEffect(() => {
    let live = true;
    listSessions()
      .then((r) => {
        if (live) setSessions(r.sessions);
      })
      .catch((e) => console.error("listSessions failed", e))
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [params.id]);

  useEffect(() => {
    if (!createdSession) return;
    setSessions((current) => {
      const next = current.filter((s) => s.id !== createdSession.id);
      next.unshift(createdSession);
      return next;
    });
    setLoading(false);
  }, [createdSession]);

  if (loading) {
    return (
      <div className="px-4 py-3 text-xs text-muted-foreground">Loading...</div>
    );
  }
  if (sessions.length === 0) {
    return (
      <div className="px-4 py-3 text-xs text-muted-foreground">
        No sessions yet.
      </div>
    );
  }

  return (
    <nav className="flex flex-col gap-1 px-2 py-2">
      {sessions.map((s) => {
        const active = s.id === params.id;
        return (
          <div
            key={s.id}
            className={cn(
              "group/session relative flex items-center rounded-[10px] pr-1 text-sidebar-foreground transition-colors",
              "hover:bg-juex-gold-100/80 hover:text-juex-forest-900 dark:hover:bg-juex-forest-700 dark:hover:text-juex-gold-200",
              active &&
                "bg-juex-gold-100 text-juex-forest-900 shadow-[inset_0_0_0_1px_rgba(239,201,124,0.55)] before:absolute before:-left-2 before:top-2 before:bottom-2 before:w-[3px] before:rounded-full before:bg-juex-gold-500 hover:bg-juex-gold-200/80 dark:bg-juex-gold-400 dark:text-juex-forest-900 dark:hover:bg-juex-gold-300 dark:hover:text-juex-forest-900",
            )}
          >
            <Link
              to={`/sessions/${encodeURIComponent(s.id)}`}
              className="min-w-0 flex-1 px-3 py-2 text-sm"
            >
              <div className="line-clamp-1">{s.preview || "(empty)"}</div>
              <div
                className={cn(
                  "mt-0.5 font-mono text-[11px] text-muted-foreground",
                  active && "text-juex-ink-600 dark:text-juex-forest-800",
                )}
              >
                {humanAgo(s.last_active_at)}
              </div>
            </Link>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              title="Delete session"
              aria-label="Delete session"
              disabled={deletingID === s.id}
              onClick={() => void handleDelete(s.id)}
              className={cn(
                "opacity-0 group-hover/session:opacity-100 focus-visible:opacity-100",
                active && "text-juex-forest-900 hover:bg-juex-gold-300/60",
              )}
            >
              <Trash2 className="size-3.5" />
            </Button>
          </div>
        );
      })}
    </nav>
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
