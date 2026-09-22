import { useEffect, useState, type FormEvent } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { ArrowRight, RefreshCw } from "lucide-react";
import {
  getMemoryDomains,
  getMemoryFacts,
  getMemoryStatus,
  type MemoryDomain,
  type MemoryFactPage,
  type MemoryFactView,
  type MemoryRelation,
  type MemoryStatus,
} from "@/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const control = "min-h-9 w-full rounded-md border bg-background px-2 text-sm";
const labels = [
  "current",
  "overdue",
  "future",
  "expired",
  "superseded",
  "corrected",
  "retracted",
  "disputed",
];
function errorText(e: unknown) {
  return e instanceof Error ? e.message : "Memory request failed.";
}
function Rule({ relation: r }: { relation: MemoryRelation }) {
  return (
    <details className="rounded-md border p-3">
      <summary className="cursor-pointer text-sm font-medium">
        <span className="inline-flex flex-wrap items-center gap-2">
          <span>{r.subjects.join(" / ")}</span>
          <ArrowRight aria-hidden className="size-3" />
          <span className="text-primary">{r.predicate}</span>
          <ArrowRight aria-hidden className="size-3" />
          <span>{r.objects?.join(" / ") || `${r.value_type} value`}</span>
        </span>
      </summary>
      <div className="mt-3 space-y-2 text-sm">
        <p>{r.description}</p>
        <p>
          <strong>
            {r.cardinality === "one" ? "Single value" : "Multiple values"}
          </strong>{" "}
          · Competes within: {r.competition.join(" + ")}
        </p>
        {r.qualifiers?.length ? (
          <p>Required qualifiers: {r.qualifiers.join(", ")}</p>
        ) : null}
        <p>
          Optional qualifiers: {r.optional_qualifiers?.join(", ") || "None"}.
          Evidence source types: {r.source_types?.join(", ") || "As declared"}.
        </p>
        <p>{r.temporal}</p>
        <p>Updates: {r.updates.join(", ")}</p>
        <p>{r.evidence}</p>
        <p>
          <strong>Example:</strong> {r.positive}
        </p>
        <p>
          <strong>Do not infer:</strong> {r.negative}
        </p>
      </div>
    </details>
  );
}
function FactDetail({ value: v }: { value: MemoryFactView }) {
  const location = useLocation(),
    f = v.fact;
  return (
    <section
      aria-label="Fact details"
      className="space-y-3 rounded-md border bg-card p-4 text-sm"
    >
      <h3 className="text-base font-semibold">Fact details · {f.id}</h3>
      <p>
        <strong>{v.subject.name}</strong> ({v.subject.id}) →{" "}
        <strong>{f.predicate}</strong> →{" "}
        {v.object
          ? `${v.object.name} (${v.object.id}, ${v.object.kind} entity)`
          : `${f.value} (literal value)`}
      </p>
      <dl className="grid min-w-0 gap-x-4 gap-y-2 break-words sm:grid-cols-[8rem_1fr]">
        <dt>Domain</dt>
        <dd>{f.domain}</dd>
        <dt>Lifecycle</dt>
        <dd>
          {v.lifecycle} · stored status: {f.status}
        </dd>
        <dt>Context</dt>
        <dd className="break-all">
          Workspace: {v.scope.workspace || "Any"} · Project:{" "}
          {v.scope.project || "Any"}
        </dd>
        <dt>Qualifiers</dt>
        <dd>
          {Object.entries(f.qualifiers || {})
            .map(([k, x]) => `${k}: ${x}`)
            .join(" · ") || "None"}
        </dd>
        <dt>Recorded</dt>
        <dd>{f.recorded_at}</dd>
        <dt>Effective</dt>
        <dd>
          {f.valid_from || "Unknown start"} → {f.valid_until || "Open end"}
        </dd>
        {f.due_at ? (
          <>
            <dt>Due</dt>
            <dd>{f.due_at} · A passed deadline does not mean completed.</dd>
          </>
        ) : null}
        {f.time_note ? (
          <>
            <dt>Time uncertainty</dt>
            <dd>{f.time_note}</dd>
          </>
        ) : null}
        <dt>Evidence reason</dt>
        <dd>{f.reason}</dd>
        <dt>Source type</dt>
        <dd>{f.source_type}</dd>
        <dt>Replaces / corrects</dt>
        <dd>
          {f.replaces?.join(", ") || "None"}
          {f.replaces?.length
            ? " · Referenced facts may be historical or unavailable after user deletion."
            : ""}
        </dd>
      </dl>
      <Link
        className="inline-block text-primary underline"
        to={`/memory/${encodeURIComponent(v.entry_id)}${location.search}`}
      >
        Open Memory entry {v.entry_id} · revision {v.revision}
      </Link>
      <details>
        <summary className="cursor-pointer font-medium">
          Fact sources ({f.sources.length})
        </summary>
        <ul className="mt-2 space-y-2 break-all text-xs">
          {f.sources.map((s, i) => (
            <li key={i}>
              Fleet {s.fleet_id} · Agent {s.agent_id} · Thread {s.thread_id} ·{" "}
              {s.generation_id} · {s.from}–{s.through}
            </li>
          ))}
        </ul>
        <p className="mt-2 text-xs text-muted-foreground">
          Raw Thread navigation is unavailable here: a source reference does not
          grant access to its original history.
        </p>
      </details>
    </section>
  );
}

export function MemoryKnowledge() {
  const [params, setParams] = useSearchParams();
  const domain = params.get("domain") ?? "identity",
    view = params.get("view") || "current";
  const [domains, setDomains] = useState<MemoryDomain[]>([]),
    [template, setTemplate] = useState<MemoryDomain | null>(null);
  const [page, setPage] = useState<MemoryFactPage | null>(null),
    [status, setStatus] = useState<MemoryStatus | null>(null);
  const [error, setError] = useState<string | null>(null),
    [loading, setLoading] = useState(true),
    [refresh, setRefresh] = useState(0);
  const [draft, setDraft] = useState({
    q: params.get("q") || "",
    source_agent_id: params.get("source_agent_id") || "",
    entity: params.get("entity") || "",
    predicate: params.get("predicate") || "",
    workspace: params.get("workspace") || "",
    project: params.get("project") || "",
    at: params.get("at") || "",
  });
  const signature = params.toString();
  useEffect(() => {
    const query = new URLSearchParams(signature);
    setDraft({
      q: query.get("q") || "",
      source_agent_id: query.get("source_agent_id") || "",
      entity: query.get("entity") || "",
      predicate: query.get("predicate") || "",
      workspace: query.get("workspace") || "",
      project: query.get("project") || "",
      at: query.get("at") || "",
    });
  }, [signature]); // URL owns navigation state.
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError(null);
    setPage(null);
    setTemplate(null);
    const query = new URLSearchParams(signature);
    query.delete("tab");
    query.delete("fact");
    query.set("domain", domain);
    query.set("limit", "20");
    Promise.all([
      getMemoryDomains(),
      domain ? getMemoryDomains(domain) : Promise.resolve([]),
      getMemoryFacts(query),
      getMemoryStatus(),
    ])
      .then(([all, full, facts, state]) => {
        if (active) {
          setDomains(all);
          setTemplate(full[0] || null);
          setPage(facts);
          setStatus(state);
        }
      })
      .catch((e) => {
        if (active) setError(errorText(e));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [signature, domain, refresh]);
  function change(values: Record<string, string>, reset = true) {
    const next = new URLSearchParams(params);
    next.set("tab", "knowledge");
    if (reset) {
      next.delete("offset");
      next.delete("fact");
    }
    for (const [key, value] of Object.entries(values)) {
      if (value || key === "domain") next.set(key, value);
      else next.delete(key);
    }
    setParams(next);
  }
  function filter(event: FormEvent) {
    event.preventDefault();
    let at = "";
    if (view === "as_of") {
      const date = new Date(draft.at);
      if (
        !draft.at ||
        !/(?:Z|[+-]\d{2}:\d{2})$/i.test(draft.at) ||
        !Number.isFinite(date.getTime())
      ) {
        setError(
          "Enter an effective time with timezone, for example 2026-01-01T00:00:00Z.",
        );
        return;
      }
      at = date.toISOString();
    }
    change({ ...draft, at });
  }
  const selected = page?.facts.find((x) => x.fact.id === params.get("fact"));
  const offset = Number(params.get("offset") || "0");
  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Memory domains</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Explore definitions and shared knowledge in this Fleet.
          </p>
        </div>
        <Button
          variant="outline"
          size="icon"
          aria-label="Refresh knowledge"
          disabled={loading}
          onClick={() => setRefresh((x) => x + 1)}
        >
          <RefreshCw className="size-4" />
        </Button>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">
          Domain
          <select
            aria-label="Domain"
            className={control}
            value={domain}
            onChange={(e) => change({ domain: e.target.value, predicate: "" })}
          >
            <option value="">All domains</option>
            {domains.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
        <label className="space-y-1 text-sm">
          View
          <select
            aria-label="Knowledge view"
            className={control}
            value={view}
            onChange={(e) =>
              change({
                view: e.target.value,
                status: "",
                at: e.target.value === "as_of" ? new Date().toISOString() : "",
              })
            }
          >
            <option value="current">Current</option>
            <option value="history">History and audit</option>
            <option value="as_of">Effective at time</option>
          </select>
        </label>
      </div>
      {error ? (
        <div
          role="alert"
          className="rounded-md border border-destructive/30 p-4 text-sm"
        >
          <p>Memory service could not load knowledge: {error}</p>
          <p className="mt-1 text-muted-foreground">
            This is not an empty result. Refresh when the service is available.
          </p>
        </div>
      ) : null}
      {loading ? (
        <p role="status" className="text-sm text-muted-foreground">
          Loading knowledge…
        </p>
      ) : null}
      {template ? (
        <section aria-label="Domain structure" className="space-y-3">
          <h2 className="font-semibold">{template.name} · Structure</h2>
          <p className="text-sm text-muted-foreground">
            {template.description}
          </p>
          <p className="text-xs text-muted-foreground">{template.policy}</p>
          <div className="grid gap-2">
            {template.relations?.map((r) => (
              <Rule key={r.predicate} relation={r} />
            ))}
          </div>
        </section>
      ) : !domain && domains.length ? (
        <p className="text-sm text-muted-foreground">
          Select any domain to inspect its structure, including domains with no
          facts.
        </p>
      ) : null}
      <section aria-label="Persisted knowledge" className="space-y-4">
        <h2 className="font-semibold">Entities and facts</h2>
        <form onSubmit={filter} className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {(
              [
                ["q", "Search facts"],
                ["source_agent_id", "Source Agent"],
                ["entity", "Entity ID"],
                ["predicate", "Relation"],
                ["workspace", "Workspace context"],
                ["project", "Project context"],
              ] as const
            ).map(([key, label]) => (
              <label key={key} className="space-y-1 text-sm">
                {label}
                <Input
                  aria-label={label}
                  value={draft[key]}
                  onChange={(e) =>
                    setDraft((x) => ({ ...x, [key]: e.target.value }))
                  }
                />
              </label>
            ))}
            {view === "history" ? (
              <label className="space-y-1 text-sm">
                Lifecycle
                <select
                  aria-label="Lifecycle"
                  className={control}
                  value={params.get("status") || ""}
                  onChange={(e) => change({ status: e.target.value })}
                >
                  <option value="">All statuses</option>
                  {labels.map((x) => (
                    <option key={x} value={x}>
                      {x}
                    </option>
                  ))}
                </select>
              </label>
            ) : null}
            {view === "as_of" ? (
              <label className="space-y-1 text-sm">
                Effective time
                <Input
                  aria-label="Effective time"
                  placeholder="2026-01-01T00:00:00Z"
                  value={draft.at}
                  onChange={(e) =>
                    setDraft((x) => ({ ...x, at: e.target.value }))
                  }
                />
              </label>
            ) : null}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button type="submit">Apply filters</Button>
            <Button
              variant="outline"
              type="button"
              onClick={() =>
                setParams({ tab: "knowledge", domain, view: "current" })
              }
            >
              Clear filters
            </Button>
          </div>
        </form>
        {status ? (
          <p className="text-xs text-muted-foreground">
            {status.pending} pending reviews · {status.running} reviewing ·{" "}
            {status.index_ready
              ? "Index ready"
              : "Committed data is available; index not ready"}
          </p>
        ) : null}
        {page ? (
          <>
            <p className="text-xs text-muted-foreground">
              {page.facts.length} facts on this page · {page.total} match the
              filters.{" "}
              {view === "as_of"
                ? "Effective-time truth, not a snapshot of what was recorded then."
                : view === "history"
                  ? "Audit includes ended, corrected and disputed claims."
                  : "Current applicable facts; overdue obligations remain open."}
            </p>
            {!page.facts.length ? (
              <p className="rounded-md border p-5 text-sm text-muted-foreground">
                {page.domain_total === 0
                  ? "No facts stored in this domain yet. Its structure remains available above."
                  : "No facts match these filters. Try history or clear the filters."}
              </p>
            ) : (
              <ul aria-label="Entity relationships" className="space-y-2">
                {page.facts.map((v) => (
                  <li key={v.fact.id} className="rounded-md border bg-card p-3">
                    <div className="grid items-center gap-2 sm:grid-cols-[1fr_auto_1fr]">
                      <Button
                        variant="outline"
                        className="h-auto min-h-9 whitespace-normal break-all"
                        onClick={() =>
                          change({ entity: v.subject.id, domain: "" })
                        }
                      >
                        {v.subject.name}{" "}
                        <span className="text-xs text-muted-foreground">
                          {v.subject.kind} · {v.subject.id}
                        </span>
                      </Button>
                      <Button
                        variant="ghost"
                        className="h-auto whitespace-normal"
                        aria-label={`Inspect fact ${v.fact.id}`}
                        onClick={() => change({ fact: v.fact.id }, false)}
                      >
                        <ArrowRight aria-hidden className="size-3" />
                        {v.fact.predicate}
                        <ArrowRight aria-hidden className="size-3" />
                      </Button>
                      {v.object ? (
                        <Button
                          variant="outline"
                          className="h-auto min-h-9 whitespace-normal break-all"
                          onClick={() =>
                            change({ entity: v.object!.id, domain: "" })
                          }
                        >
                          {v.object.name}
                          <span className="text-xs text-muted-foreground">
                            {v.object.kind} · {v.object.id}
                          </span>
                        </Button>
                      ) : (
                        <p className="min-w-0 break-words rounded-md bg-muted/50 p-2 text-sm">
                          {v.fact.value}{" "}
                          <span className="text-xs text-muted-foreground">
                            literal
                          </span>
                        </p>
                      )}
                    </div>
                    <p className="mt-2 break-all text-xs text-muted-foreground">
                      {v.fact.domain} · {v.lifecycle} ·{" "}
                      {v.scope.project ||
                        v.scope.workspace ||
                        "No specific context"}{" "}
                      · {v.fact.id}
                    </p>
                    {params.get("fact") === v.fact.id ? (
                      <div className="mt-3">
                        <FactDetail value={v} />
                      </div>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
            {!selected && params.get("fact") ? (
              <p role="status" className="text-sm">
                The selected fact is no longer on this page. Refresh or adjust
                the filters.
              </p>
            ) : null}
            {offset > 0 || page.next >= 0 ? (
              <div className="flex items-center justify-between gap-2">
                <Button
                  variant="outline"
                  disabled={offset <= 0}
                  onClick={() =>
                    change(
                      { offset: String(Math.max(0, offset - 20)), fact: "" },
                      false,
                    )
                  }
                >
                  Previous facts
                </Button>
                <span className="text-xs">
                  Page {Math.floor(offset / 20) + 1}
                </span>
                <Button
                  variant="outline"
                  disabled={page.next < 0}
                  onClick={() =>
                    change({ offset: String(page.next), fact: "" }, false)
                  }
                >
                  Next facts
                </Button>
              </div>
            ) : null}
          </>
        ) : null}
      </section>
    </div>
  );
}
