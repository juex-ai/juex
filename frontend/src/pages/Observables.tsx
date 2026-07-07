import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Pause, Play, RefreshCw, Trash2 } from "lucide-react";

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

const observableGridColumns =
  "grid-cols-[24rem_minmax(8rem,0.6fr)_minmax(18rem,1.2fr)_minmax(18rem,1fr)_8rem]";
const observableGridMinWidth = "min-w-[76rem]";

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
            <div
              className={cn("w-full text-left text-sm", observableGridMinWidth)}
              role="table"
              aria-label="Observables"
            >
              <div
                className={cn(
                  "grid bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground",
                  observableGridColumns,
                )}
                role="row"
              >
                <div className="px-3 py-2 font-medium" role="columnheader">
                  Observable
                </div>
                <div className="px-3 py-2 font-medium" role="columnheader">
                  State
                </div>
                <div className="px-3 py-2 font-medium" role="columnheader">
                  Source
                </div>
                <div className="px-3 py-2 font-medium" role="columnheader">
                  Last Observation
                </div>
                <div className="px-3 py-2 text-right font-medium" role="columnheader">
                  Actions
                </div>
              </div>
              <div role="rowgroup">
                {observables.map((item) => (
                  <ObservableRow
                    key={item.id}
                    item={item}
                    busy={busyID === item.id}
                    onAction={runAction}
                  />
                ))}
              </div>
            </div>
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
  const detailHref = `/observables/${encodeURIComponent(item.id)}`;
  const detailLabel = `Open observable ${item.name || item.id}`;

  return (
    <div
      className={cn(
        "group relative grid cursor-pointer border-t transition-colors hover:bg-muted/35 focus-within:bg-muted/40",
        observableGridColumns,
      )}
      role="row"
    >
      <Link
        to={detailHref}
        className="absolute inset-0 z-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35"
        aria-label={detailLabel}
      />
      <div className="pointer-events-none relative z-10 min-w-0 px-3 py-2" role="cell">
        <div className="pointer-events-none relative z-10 min-w-0 rounded-md px-1 py-1">
          <span className="min-w-0">
            <span className="block truncate font-medium text-foreground">
              {item.name || item.id}
            </span>
            <span className="block truncate font-mono text-[11px] text-muted-foreground">
              {item.id}
            </span>
          </span>
        </div>
      </div>
      <div className="pointer-events-none relative z-10 px-3 py-2" role="cell">
        <StateBadge state={item.state} />
      </div>
      <div className="pointer-events-none relative z-10 min-w-0 px-3 py-2" role="cell">
        <div className="pointer-events-none relative z-10 min-w-0">
          <div className="flex items-center gap-1.5">
            <Badge variant="outline" className="font-mono text-[11px]">
              {item.source_type || "command"}
            </Badge>
            <span className="truncate font-mono text-xs">
              {sourceSummary(item)}
            </span>
          </div>
          {item.source_type === "schedule" ? (
            <div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
              next {formatDateTime(item.schedule?.next_occurrence)}
            </div>
          ) : null}
        </div>
      </div>
      <div className="pointer-events-none relative z-10 min-w-0 px-3 py-2" role="cell">
        {last ? (
          <div className="pointer-events-none relative z-10 min-w-0">
            <div className="truncate text-sm">{last.content || "-"}</div>
            <div className="mt-1 flex flex-wrap items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
              <span>{last.kind}</span>
              <span>{last.severity}</span>
              <span>{humanAgo(last.created_at)}</span>
            </div>
          </div>
        ) : (
          <span className="pointer-events-none relative z-10 text-muted-foreground">-</span>
        )}
      </div>
      <div className="relative z-20 cursor-default px-3 py-2" role="cell">
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
      </div>
    </div>
  );
}

function sourceSummary(item: ObservableStatus): string {
  if (item.source_type === "schedule") {
    return item.schedule?.summary || "schedule";
  }
  return [item.command, ...(item.args ?? [])].filter(Boolean).join(" ") || "command";
}

function formatDateTime(iso?: string): string {
  if (!iso) return "-";
  const date = new Date(iso);
  if (!Number.isFinite(date.getTime())) return iso;
  return date.toLocaleString();
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

export function humanAgo(value?: number | string): string {
  if (value === undefined || value === "") return "-";
  const t = new Date(value).getTime();
  if (!Number.isFinite(t)) return String(value);
  const diff = Date.now() - t;
  const sec = Math.max(0, Math.round(diff / 1000));
  if (sec < 60) return "just now";
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d ago`;
  return new Date(value).toLocaleDateString();
}
