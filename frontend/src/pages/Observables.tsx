import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Activity, Pause, Play, RefreshCw, Trash2 } from "lucide-react";

import {
  deleteObservable,
  listObservables,
  startObservable,
  stopObservable,
} from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { ObservableStatus } from "@/types";

export function Observables() {
  const navigate = useNavigate();
  const [observables, setObservables] = useState<ObservableStatus[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busyID, setBusyID] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  useShellTitle("Observables");

  const refresh = useCallback(async (
    { quiet = false }: { quiet?: boolean } = {},
  ) => {
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      const data = await listObservables();
      setObservables(data.observables ?? []);
    } catch (e) {
      console.error("listObservables failed", e);
      setError(e instanceof Error ? e.message : "Failed to load observables.");
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    let live = true;
    let timer: number | undefined;
    const load = async () => {
      if (!live) return;
      await refresh({ quiet: true });
      if (live) timer = window.setTimeout(load, 3000);
    };
    void load();
    return () => {
      live = false;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [refresh]);

  async function runAction(
    id: string,
    action: "start" | "stop" | "delete",
  ) {
    if (action === "delete" && !window.confirm(`Delete observable "${id}"?`)) {
      return;
    }
    setBusyID(id);
    setError(null);
    try {
      if (action === "start") {
        await startObservable(id);
      } else if (action === "stop") {
        await stopObservable(id);
      } else {
        await deleteObservable(id);
      }
      await refresh({ quiet: true });
      if (action === "delete") navigate("/observables", { replace: true });
    } catch (e) {
      console.error(`${action}Observable failed`, e);
      setError(e instanceof Error ? e.message : `Failed to ${action}.`);
    } finally {
      setBusyID(null);
    }
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <h1 className="text-xl font-semibold text-foreground">
              Observables
            </h1>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => void refresh()}
            disabled={refreshing}
          >
            <RefreshCw className={cn("size-3.5", refreshing && "animate-spin")} />
            Refresh
          </Button>
        </div>
        {error ? (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        ) : null}
        <div className="overflow-x-auto rounded-md border bg-card shadow-[var(--shadow-xs)]">
          {loading ? (
            <div className="px-4 py-6 text-sm text-muted-foreground">
              Loading observables...
            </div>
          ) : observables.length === 0 ? (
            <div className="px-4 py-10 text-center text-sm text-muted-foreground">
              No observables configured.
            </div>
          ) : (
            <table className="w-full min-w-[58rem] text-left text-sm">
              <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Observable</th>
                  <th className="px-3 py-2 font-medium">State</th>
                  <th className="px-3 py-2 font-medium">Command</th>
                  <th className="px-3 py-2 font-medium">Last Observation</th>
                  <th className="px-3 py-2 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                {observables.map((item) => (
                  <ObservableRow
                    key={item.id}
                    item={item}
                    busy={busyID === item.id}
                    onAction={runAction}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function ObservableRow({
  item,
  busy,
  onAction,
}: {
  item: ObservableStatus;
  busy: boolean;
  onAction: (id: string, action: "start" | "stop" | "delete") => Promise<void>;
}) {
  const last = item.last_observation?.id ? item.last_observation : null;
  return (
    <tr className="border-t">
      <td className="px-3 py-2">
        <Link
          to={`/observables/${encodeURIComponent(item.id)}`}
          className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-2 rounded-md px-1 py-1 outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/35"
        >
          <span className="flex size-8 items-center justify-center rounded-md bg-primary/10 text-primary">
            <Activity className="size-4" />
          </span>
          <span className="min-w-0">
            <span className="block truncate font-medium text-foreground">
              {item.name || item.id}
            </span>
            <span className="block truncate font-mono text-[11px] text-muted-foreground">
              {item.id}
            </span>
          </span>
        </Link>
      </td>
      <td className="px-3 py-2">
        <StateBadge state={item.state} />
      </td>
      <td className="max-w-[24rem] truncate px-3 py-2 font-mono text-xs">
        {[item.command, ...(item.args ?? [])].join(" ")}
      </td>
      <td className="max-w-[22rem] px-3 py-2">
        {last ? (
          <div className="min-w-0">
            <div className="truncate text-sm">{last.content || "-"}</div>
            <div className="mt-1 flex flex-wrap items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
              <span>{last.kind}</span>
              <span>{last.severity}</span>
              <span>{humanAgo(last.created_at)}</span>
            </div>
          </div>
        ) : (
          <span className="text-muted-foreground">-</span>
        )}
      </td>
      <td className="px-3 py-2">
        <div className="flex justify-end gap-1">
          {item.state === "running" ? (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              title="Stop"
              aria-label="Stop observable"
              disabled={busy}
              onClick={() => void onAction(item.id, "stop")}
            >
              <Pause className="size-3.5" />
            </Button>
          ) : (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              title="Start"
              aria-label="Start observable"
              disabled={busy}
              onClick={() => void onAction(item.id, "start")}
            >
              <Play className="size-3.5" />
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            title="Delete"
            aria-label="Delete observable"
            disabled={busy}
            onClick={() => void onAction(item.id, "delete")}
            className="text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      </td>
    </tr>
  );
}

export function StateBadge({ state }: { state: string }) {
  const variant: "destructive" | "secondary" | "outline" =
    state === "errored"
      ? "destructive"
      : state === "running"
        ? "secondary"
        : "outline";
  return (
    <Badge variant={variant} className="font-mono text-[11px]">
      {state || "-"}
    </Badge>
  );
}

export function humanAgo(iso?: string): string {
  if (!iso) return "-";
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return iso;
  const diff = Date.now() - t;
  const sec = Math.max(0, Math.round(diff / 1000));
  if (sec < 60) return "just now";
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d ago`;
  return new Date(iso).toLocaleDateString();
}
