import { useEffect, useState, type FormEvent } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import {
  ArrowRight,
  Pencil,
  RefreshCw,
  SlidersHorizontal,
  X,
} from "lucide-react";
import {
  getMemoryDomains,
  getMemoryFacts,
  getMemoryStatus,
  type MemoryDomain,
  type MemoryFactPage,
  type MemoryFactView,
  type MemoryStatus,
} from "@/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { DomainStructure } from "@/components/memory/DomainStructure";

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
function FactDetail({ value: v }: { value: MemoryFactView }) {
  const location = useLocation(),
    f = v.fact;
  return (
    <section
      aria-label="Fact details"
      className="min-w-0 space-y-3 break-words rounded-md border bg-muted/20 p-4 text-sm"
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
  const [structureOpen, setStructureOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const signature = params.toString();
  const requestParams = new URLSearchParams(params);
  requestParams.delete("tab");
  requestParams.delete("fact");
  requestParams.set("domain", domain);
  requestParams.set("limit", "20");
  const requestSignature = requestParams.toString();
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
    // Keep the same domain's controls mounted while filtering to retain focus.
    const query = new URLSearchParams(requestSignature);
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
  }, [requestSignature, domain, refresh]);
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
  const filters = [
    ["q", "Search"],
    ["predicate", "Relation"],
    ["source_agent_id", "Source Agent"],
    ["entity", "Entity"],
    ["workspace", "Workspace"],
    ["project", "Project"],
  ] as const;
  const advancedCount = filters
    .slice(2)
    .filter(([key]) => params.get(key)).length;
  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Memory domains</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Explore relationships. Find and refine shared knowledge.
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
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[19rem_1fr]">
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
        <label className="space-y-1 text-sm sm:max-w-64">
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
      <div className="grid items-start gap-5 lg:grid-cols-[19rem_minmax(0,1fr)]">
        {template?.id === domain && !error ? (
          <DomainStructure
            domain={template}
            predicate={params.get("predicate") || ""}
            onSelect={(predicate) => change({ predicate })}
            open={structureOpen}
            onToggle={() => setStructureOpen((x) => !x)}
          />
        ) : (
          <div className="rounded-lg border p-4 text-sm text-muted-foreground">
            {!domain
              ? "Select any domain to inspect its structure, including domains with no facts."
              : error
                ? "Domain structure is unavailable."
                : "Loading domain structure…"}
          </div>
        )}
        <section aria-label="Persisted knowledge" className="min-w-0 space-y-4">
          <div className="flex items-center justify-between gap-2">
            <h2 className="font-semibold">Entities and facts</h2>
            {page ? (
              <span className="text-xs text-muted-foreground">
                {page.total} matching
              </span>
            ) : null}
          </div>
          <form onSubmit={filter} className="space-y-3">
            <div className="flex gap-2">
              <Input
                aria-label="Search facts"
                placeholder="Search facts…"
                value={draft.q}
                onChange={(e) => setDraft((x) => ({ ...x, q: e.target.value }))}
              />
              <Button type="submit">Apply filters</Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                type="button"
                aria-expanded={advancedOpen}
                aria-controls="memory-advanced-filters"
                onClick={() => setAdvancedOpen((x) => !x)}
              >
                <SlidersHorizontal className="size-3.5" />
                Advanced filters{advancedCount ? ` (${advancedCount})` : ""}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                type="button"
                onClick={() =>
                  setParams({ tab: "knowledge", domain, view: "current" })
                }
              >
                Clear filters
              </Button>
            </div>
            {advancedOpen ? (
              <div
                id="memory-advanced-filters"
                className="grid gap-3 rounded-lg border bg-muted/20 p-3 sm:grid-cols-2"
              >
                {(
                  [
                    ["source_agent_id", "Source Agent"],
                    ["entity", "Entity ID"],
                    ["predicate", "Relation"],
                    ["workspace", "Workspace context"],
                    ["project", "Project context"],
                  ] as const
                ).map(([key, label]) => (
                  <label key={key} className="space-y-1 text-xs">
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
              </div>
            ) : null}
            {view === "history" ? (
              <label className="block max-w-64 space-y-1 text-sm">
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
              <label className="block space-y-1 text-sm">
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
          </form>
          {filters.some(([key]) => params.get(key)) ? (
            <div aria-label="Active filters" className="flex flex-wrap gap-2">
              {filters.map(([key, label]) =>
                params.get(key) ? (
                  <Button
                    key={key}
                    variant="secondary"
                    size="sm"
                    className="max-w-full"
                    aria-label={`Remove ${label} filter: ${params.get(key)}`}
                    title={`${label}: ${params.get(key)}`}
                    onClick={() => change({ [key]: "" })}
                  >
                    <span className="truncate">
                      {label}: {params.get(key)}
                    </span>
                    <X aria-hidden className="size-3 shrink-0" />
                  </Button>
                ) : null,
              )}
            </div>
          ) : null}
          {page ? (
            <>
              <p className="text-xs text-muted-foreground">
                {page.facts.length} facts on this page ·{" "}
                {view === "as_of"
                  ? "Effective-time truth, not a snapshot of what was recorded then."
                  : view === "history"
                    ? "Audit includes ended, corrected and disputed claims."
                    : "Current applicable facts; overdue obligations remain open."}
              </p>
              {!page.facts.length ? (
                <p className="rounded-lg border bg-card p-5 text-sm text-muted-foreground">
                  {page.domain_total === 0
                    ? "No facts stored in this domain yet. Its structure remains available in Domain structure."
                    : "No facts match these filters. Try history or clear the filters."}
                </p>
              ) : (
                <ul aria-label="Entity relationships" className="space-y-3">
                  {page.facts.map((v) => (
                    <li
                      key={v.fact.id}
                      className="min-w-0 rounded-lg border bg-card p-4"
                    >
                      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        <span className="rounded bg-muted px-2 py-0.5 font-medium">
                          {v.lifecycle}
                        </span>
                        <span>{v.fact.domain}</span>
                        <span className="break-all">
                          {v.scope.project ||
                            v.scope.workspace ||
                            "No specific context"}
                        </span>
                      </div>
                      <div className="my-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
                        <button
                          type="button"
                          className="max-w-full break-words text-left font-semibold text-primary underline-offset-4 hover:underline focus-visible:underline"
                          title={`${v.subject.kind} · ${v.subject.id}`}
                          aria-label={`Explore entity ${v.subject.name} (${v.subject.id})`}
                          onClick={() =>
                            change({ entity: v.subject.id, domain: "" })
                          }
                        >
                          {v.subject.name}
                        </button>
                        <ArrowRight
                          aria-hidden
                          className="size-3 shrink-0 text-muted-foreground"
                        />
                        <span className="break-words text-muted-foreground">
                          {v.fact.predicate.replaceAll("_", " ")}
                        </span>
                        <ArrowRight
                          aria-hidden
                          className="size-3 shrink-0 text-muted-foreground"
                        />
                        {v.object ? (
                          <button
                            type="button"
                            className="max-w-full break-words text-left font-semibold text-primary underline-offset-4 hover:underline focus-visible:underline"
                            title={`${v.object.kind} · ${v.object.id}`}
                            aria-label={`Explore entity ${v.object.name} (${v.object.id})`}
                            onClick={() =>
                              change({ entity: v.object!.id, domain: "" })
                            }
                          >
                            {v.object.name}
                          </button>
                        ) : (
                          <span className="max-w-full break-words font-medium">
                            {v.fact.value}
                          </span>
                        )}
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          aria-label={`Inspect fact ${v.fact.id}`}
                          aria-expanded={params.get("fact") === v.fact.id}
                          onClick={() =>
                            change(
                              {
                                fact:
                                  params.get("fact") === v.fact.id
                                    ? ""
                                    : v.fact.id,
                              },
                              false,
                            )
                          }
                        >
                          Details
                        </Button>
                        <Button asChild variant="ghost" size="sm">
                          <Link
                            aria-label={`Edit memory for fact ${v.fact.id}`}
                        to={`/memory/${encodeURIComponent(v.entry_id)}${memoryEditSearch(params, v.fact.id)}`}
                          >
                            <Pencil className="size-3.5" />
                            Edit memory
                          </Link>
                        </Button>
                      </div>
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
          {status ? (
            <p className="text-xs text-muted-foreground">
              {status.pending} pending reviews · {status.running} reviewing ·{" "}
              {status.index_ready
                ? "Index ready"
                : "Committed data is available; index not ready"}
            </p>
          ) : null}
        </section>
      </div>
    </div>
  );
}

function memoryEditSearch(params: URLSearchParams, fact: string) {
  const next = new URLSearchParams(params);
  next.set("fact", fact);
  next.set("edit", "1");
  return `?${next}`;
}
