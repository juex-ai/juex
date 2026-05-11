import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Trash2 } from "lucide-react";
import { deleteSession, listSessions } from "@/api";
import { Button } from "@/components/ui/button";
import type { SessionInfo } from "@/types";
import { cn } from "@/lib/utils";

export function SidebarSessionList() {
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
  }, []);

  if (loading) {
    return (
      <div className="text-muted-foreground text-xs px-3 py-2">Loading…</div>
    );
  }
  if (sessions.length === 0) {
    return (
      <div className="text-muted-foreground text-xs px-3 py-2">
        No sessions yet.
      </div>
    );
  }

  return (
    <nav className="flex flex-col gap-0.5 px-2 py-1">
      {sessions.map((s) => (
        <div
          key={s.id}
          className={cn(
            "group/session flex items-center rounded-md pr-1 hover:bg-muted/60 transition-colors",
            s.id === params.id && "bg-muted",
          )}
        >
          <Link
            to={`/sessions/${encodeURIComponent(s.id)}`}
            className="min-w-0 flex-1 px-2 py-1.5 text-sm"
          >
            <div className="line-clamp-1">{s.preview || "(empty)"}</div>
            <div className="text-muted-foreground mt-0.5 text-xs">
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
            className="opacity-0 group-hover/session:opacity-100 focus-visible:opacity-100"
          >
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      ))}
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
