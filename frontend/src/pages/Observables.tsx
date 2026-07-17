import { useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Pause, Play, RefreshCw, Trash2, Zap } from "lucide-react";

import {
  deleteObservable,
  listObservables,
  runObservable,
  startObservable,
  stopObservable,
} from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { ObservableStatus } from "@/types";

const observableGridColumns =
  "grid-cols-[minmax(10rem,1.15fr)_5.5rem_minmax(10rem,1fr)_minmax(10rem,1fr)_8rem]";
const observableGridMinWidth = "min-w-[44rem]";

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
    action: "run" | "start" | "stop" | "delete",
  ) {
    if (action === "delete" && !window.confirm(`Delete observable "${id}"?`)) {
      return;
    }
    setBusyID(id);
    setError(null);
    try {
      if (action === "run") {
        await runObservable(id);
      } else if (action === "start") {
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
            <TooltipProvider delayDuration={300}>
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
                  <div
                    className="sticky right-0 z-20 border-l bg-muted/95 px-3 py-2 text-right font-medium"
                    role="columnheader"
                  >
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
            </TooltipProvider>
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
  onAction: (
    id: string,
    action: "run" | "start" | "stop" | "delete",
  ) => Promise<void>;
}) {
  const last = item.last_observation?.id ? item.last_observation : null;
  const detailHref = `/observables/${encodeURIComponent(item.id)}`;
  const detailLabel = `Open observable ${item.name || item.id}`;
  const tooltipContentRef = useRef<HTMLDivElement>(null);

  return (
    <div
      className={cn(
        "group relative grid cursor-pointer border-t transition-colors hover:bg-muted/35 focus-within:bg-muted/40",
        observableGridColumns,
      )}
      role="row"
    >
      <Tooltip>
        <TooltipTrigger asChild>
          <Link
            to={detailHref}
            className="absolute inset-0 z-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35"
            aria-label={detailLabel}
            aria-keyshortcuts="ArrowUp ArrowDown PageUp PageDown Home End"
            onKeyDown={(event) => {
              scrollTooltipContent(event, tooltipContentRef.current);
            }}
          />
        </TooltipTrigger>
        <TooltipContent
          ref={tooltipContentRef}
          side="top"
          className="block max-h-64 max-w-md overscroll-contain overflow-y-auto whitespace-normal break-words px-3 py-2"
        >
          <div className="space-y-1.5 text-xs">
            <div>
              <span className="font-semibold">Observable: </span>
              <span>{item.name || item.id}</span>
            </div>
            <div className="font-mono text-[11px]">{item.id}</div>
            <div>
              <span className="font-semibold">Source: </span>
              <span className="font-mono">{sourceSummary(item)}</span>
            </div>
            {last ? (
              <div>
                <span className="font-semibold">Last observation: </span>
                <span>{last.content || "-"}</span>
              </div>
            ) : null}
            <div className="border-t border-background/20 pt-1 text-[10px] opacity-75">
              Use Up/Down or Page Up/Down while the row is focused to scroll.
            </div>
          </div>
        </TooltipContent>
      </Tooltip>
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
      <div
        className="sticky right-0 z-20 cursor-default border-l bg-card px-3 py-2 group-hover:bg-muted/35"
        role="cell"
      >
        <div className="flex justify-end gap-1">
          {item.source_type === "schedule" ? (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              title="Run"
              aria-label="Run schedule now"
              disabled={busy}
              onClick={() => void onAction(item.id, "run")}
            >
              <Zap className="size-3.5" />
            </Button>
          ) : null}
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

function scrollTooltipContent(
  event: KeyboardEvent<HTMLAnchorElement>,
  content: HTMLDivElement | null,
) {
  if (!content || content.scrollHeight <= content.clientHeight) return;

  let nextTop: number;
  switch (event.key) {
    case "ArrowDown":
      nextTop = content.scrollTop + 40;
      break;
    case "ArrowUp":
      nextTop = content.scrollTop - 40;
      break;
    case "PageDown":
      nextTop = content.scrollTop + content.clientHeight;
      break;
    case "PageUp":
      nextTop = content.scrollTop - content.clientHeight;
      break;
    case "Home":
      nextTop = 0;
      break;
    case "End":
      nextTop = content.scrollHeight;
      break;
    default:
      return;
  }
  event.preventDefault();
  content.scrollTo({ top: nextTop, behavior: "smooth" });
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

export function humanAgo(value?: number | string | null): string {
  if (
    value === undefined ||
    value === null ||
    value === "" ||
    value === 0 ||
    value === "0"
  ) {
    return "-";
  }
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
