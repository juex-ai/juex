import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Pause, Play, RefreshCw, Trash2, Zap } from "lucide-react";

import {
  deleteObservable,
  getObservable,
  runObservable,
  startObservable,
  stopObservable,
} from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  formatObservationTimestamp,
  formatObservationWindow,
} from "@/lib/observation-time";
import { cn } from "@/lib/utils";
import type { ObservableDetailResponse, ObservationRecord } from "@/types";
import { StateBadge } from "@/pages/Observables";
import { agentPathFromLocation } from "@/lib/fleet-routes";

export function ObservableDetail() {
  const { id = "" } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [data, setData] = useState<ObservableDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useShellTitle(data?.observable.name || data?.observable.id || "Observable");

  const refresh = useCallback(async (
    { quiet = false }: { quiet?: boolean } = {},
  ) => {
    if (!id) return;
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      const next = await getObservable(id);
      setData(next);
    } catch (e) {
      console.error("getObservable failed", e);
      setError(e instanceof Error ? e.message : "Failed to load observable.");
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [id]);

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

  async function runAction(action: "run" | "start" | "stop" | "delete") {
    if (!id) return;
    if (action === "delete" && !window.confirm(`Delete observable "${id}"?`)) {
      return;
    }
    setBusy(true);
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
        navigate(agentPathFromLocation("/observables"), { replace: true });
        return;
      }
      await refresh({ quiet: true });
    } catch (e) {
      console.error(`${action}Observable failed`, e);
      setError(e instanceof Error ? e.message : `Failed to ${action}.`);
    } finally {
      setBusy(false);
    }
  }

  if (loading && !data) {
    return (
      <div className="px-4 py-6 text-sm text-muted-foreground">
        Loading observable...
      </div>
    );
  }

  const observable = data?.observable;
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-5 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2">
            <Button asChild variant="ghost" size="icon-sm">
              <Link
                to={agentPathFromLocation("/observables")}
                aria-label="Back to observables"
              >
                <ArrowLeft className="size-3.5" />
              </Link>
            </Button>
            <div className="min-w-0">
              <h1 className="truncate text-xl font-semibold text-foreground">
                {observable?.name || observable?.id || id}
              </h1>
              <div className="mt-1 font-mono text-[11px] text-muted-foreground">
                {id}
              </div>
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-1">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void refresh()}
              disabled={refreshing}
            >
              <RefreshCw
                className={cn("size-3.5", refreshing && "animate-spin")}
              />
              Refresh
            </Button>
            {observable?.source_type === "schedule" ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => void runAction("run")}
                disabled={busy}
              >
                <Zap className="size-3.5" />
                Run
              </Button>
            ) : null}
            {observable?.state === "running" ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => void runAction("stop")}
                disabled={busy}
              >
                <Pause className="size-3.5" />
                Stop
              </Button>
            ) : (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => void runAction("start")}
                disabled={busy || !observable}
              >
                <Play className="size-3.5" />
                Start
              </Button>
            )}
            <Button
              type="button"
              variant="destructive"
              size="sm"
              onClick={() => void runAction("delete")}
              disabled={busy || !observable}
            >
              <Trash2 className="size-3.5" />
              Delete
            </Button>
          </div>
        </div>
        {error ? (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        ) : null}
        {observable ? (
          <>
            <section className="space-y-3">
              <h2 className="text-sm font-semibold text-foreground">Status</h2>
              <div className="overflow-hidden rounded-md border bg-card shadow-[var(--shadow-xs)]">
                <dl className="grid text-sm sm:grid-cols-[10rem_minmax(0,1fr)]">
                  <DetailRow label="State">
                    <StateBadge state={observable.state} />
                  </DetailRow>
                  <DetailRow label="Source">
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <Badge variant="outline" className="font-mono text-[11px]">
                        {observable.source_type || "command"}
                      </Badge>
                      <span className="break-all font-mono text-xs">
                        {detailSourceSummary(observable)}
                      </span>
                    </div>
                  </DetailRow>
                  {observable.source_type === "schedule" ? (
                    <>
                      <DetailRow label="Next">
                        <span className="font-mono text-xs">
                          {formatDateTime(observable.schedule?.next_occurrence)}
                        </span>
                      </DetailRow>
                      <DetailRow label="Last emitted">
                        <span className="font-mono text-xs">
                          {formatDateTime(
                            observable.schedule?.last_emitted_scheduled_at,
                          )}
                        </span>
                      </DetailRow>
                      <DetailRow label="Catch-up">
                        <span className="font-mono text-xs">
                          {observable.schedule?.catch_up_mode || "-"}
                        </span>
                      </DetailRow>
                    </>
                  ) : (
                    <>
                      <DetailRow label="Streams">
                        <span className="font-mono text-xs">
                          {(observable.streams ?? []).join(", ") || "-"}
                        </span>
                      </DetailRow>
                      <DetailRow label="Batch">
                        <span className="font-mono text-xs">
                          {observable.batch?.interval_seconds ?? "-"}s /{" "}
                          {observable.batch?.max_chars ?? "-"} chars
                        </span>
                      </DetailRow>
                    </>
                  )}
                  <DetailRow label="Run">
                    <span className="break-all font-mono text-xs">
                      {observable.run_id || "-"}
                    </span>
                  </DetailRow>
                  <DetailRow label="PID">
                    <span className="font-mono text-xs">
                      {observable.pid || "-"}
                    </span>
                  </DetailRow>
                  <DetailRow label="Last error">
                    <span className="break-words font-mono text-xs text-destructive">
                      {observable.last_error || "-"}
                    </span>
                  </DetailRow>
                </dl>
              </div>
            </section>
            <section className="space-y-3">
              <div className="flex items-center justify-between gap-3">
                <h2 className="text-sm font-semibold text-foreground">
                  Recent Observations
                </h2>
                <Badge variant="outline" className="font-mono text-[11px]">
                  {data?.observations?.length ?? 0}
                </Badge>
              </div>
              <ObservationList observations={data?.observations ?? []} />
            </section>
          </>
        ) : (
          <div className="rounded-md border bg-card px-4 py-6 text-sm text-muted-foreground">
            Observable not found.
          </div>
        )}
      </div>
    </div>
  );
}

function DetailRow({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <>
      <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground first:border-t-0">
        {label}
      </dt>
      <dd className="border-t px-3 py-2 first:border-t-0">{children}</dd>
    </>
  );
}

function ObservationList({
  observations,
}: {
  observations: ObservationRecord[];
}) {
  if (observations.length === 0) {
    return (
      <div className="rounded-md border bg-card px-4 py-10 text-center text-sm text-muted-foreground">
        No observations yet.
      </div>
    );
  }
  return (
    <div className="overflow-hidden rounded-md border bg-card shadow-[var(--shadow-xs)]">
      <div className="divide-y">
        {observations.map((record) => (
          <div key={record.id} className="px-3 py-3">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge
                variant={
                  record.severity === "error" ||
                  record.severity === "critical"
                    ? "destructive"
                    : "outline"
                }
                className="font-mono text-[11px]"
              >
                {record.severity || "-"}
              </Badge>
              <Badge variant="outline" className="font-mono text-[11px]">
                {record.kind || "-"}
              </Badge>
              <Badge variant="outline" className="font-mono text-[11px]">
                {record.state || "-"}
              </Badge>
              <span className="font-mono text-[11px] text-muted-foreground">
                {formatObservationTimestamp(record.created_at)}
              </span>
            </div>
            <pre className="mt-2 max-h-44 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/60 px-3 py-2 font-mono text-xs text-foreground">
              {record.content || "-"}
            </pre>
            <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 font-mono text-[11px] text-muted-foreground">
              <span>{record.id}</span>
              <span>
                window {formatObservationWindow(record.window_start, record.window_end)}
              </span>
              {record.truncated ? (
                <span>truncated {record.original_chars} chars</span>
              ) : null}
              {record.delivered_at ? (
                <span>delivered {formatObservationTimestamp(record.delivered_at)}</span>
              ) : null}
              {record.source_event_id ? <span>{record.source_event_id}</span> : null}
              {record.artifact_path ? <span>{record.artifact_path}</span> : null}
              {record.error ? <span className="text-destructive">{record.error}</span> : null}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function detailSourceSummary(observable: ObservableDetailResponse["observable"]): string {
  if (observable.source_type === "schedule") {
    return observable.schedule?.summary || "schedule";
  }
  return [observable.command, ...(observable.args ?? [])]
    .filter(Boolean)
    .join(" ") || "command";
}

function formatDateTime(iso?: string): string {
  if (!iso) return "-";
  const date = new Date(iso);
  if (!Number.isFinite(date.getTime())) return iso;
  return date.toLocaleString();
}
