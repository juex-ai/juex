import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { listSessions } from "@/api";
import type { SessionInfo } from "@/types";
import { cn } from "@/lib/utils";

export function SidebarSessionList() {
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const params = useParams<{ id?: string }>();

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
        <Link
          key={s.id}
          to={`/sessions/${encodeURIComponent(s.id)}`}
          className={cn(
            "rounded-md px-2 py-1.5 text-sm hover:bg-muted/60 transition-colors",
            s.id === params.id && "bg-muted",
          )}
        >
          <div className="line-clamp-1">{s.preview || "(empty)"}</div>
          <div className="text-muted-foreground mt-0.5 text-xs">
            {humanAgo(s.last_active_at)}
          </div>
        </Link>
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
