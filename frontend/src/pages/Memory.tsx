import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ArrowLeft, RefreshCw, Search } from "lucide-react";
import { nanoid } from "nanoid";
import { APIError, changeMemory, getMemoryStatus, readMemory, searchMemories, type MemoryEntry, type MemoryMutation, type MemoryPage, type MemoryStatus } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

const fieldClass = "grid min-w-0 gap-1.5 text-sm font-medium";
const controlClass = "h-9 w-full rounded-md border bg-background px-3 text-sm";
const userControlNotice = "Saving or deleting ends unfinished Memory reviews so they cannot overwrite your changes.";
function message(error: unknown) { return error instanceof Error ? error.message : "Memory request failed."; }
function scope(entry: MemoryEntry) { return [entry.scope.workspace, entry.scope.project].filter(Boolean).join(" · ") || "Fleet-wide"; }
function ErrorMessage({ text }: { text: string | null }) { return text ? <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive break-words">{text}</p> : null; }

export function Memory() {
  useShellTitle("Memory");
  const { entryId } = useParams();
  return <main className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6"><div className="mx-auto w-full max-w-4xl">
    {entryId ? <MemoryDetail key={entryId} id={entryId} /> : <MemoryList />}
  </div></main>;
}

function MemoryList() {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const query = params.get("q") || "";
  const rawOffset = Number(params.get("offset") || 0);
  const offset = Number.isSafeInteger(rawOffset) && rawOffset >= 0 ? rawOffset : 0;
  const [search, setSearch] = useState(query);
  const [refresh, setRefresh] = useState(0);
  const [data, setData] = useState<{ page: MemoryPage; status: MemoryStatus } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  useEffect(() => { setSearch(query); }, [query]);
  useEffect(() => {
    let active = true;
    setLoading(true); setError(null); setData(null);
    Promise.all([searchMemories(query, offset), getMemoryStatus()]).then(([page, status]) => {
      if (active) setData({ page, status });
    }).catch(cause => { if (active) setError(message(cause)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [query, offset, refresh]);
  function searchSubmit(event: FormEvent) {
    event.preventDefault();
    const text = search.trim(), bytes = new TextEncoder().encode(text).length;
    if (bytes > 2048) { setError(`Search is too long (${bytes} bytes; maximum 2048). Shorten the query before searching.`); return; }
    setError(null); setParams(text ? { q: text } : {});
  }
  function pageTo(next: number) { setParams({ ...(query ? { q: query } : {}), ...(next ? { offset: String(next) } : {}) }); }
  const entries = data?.page.entries ?? [];
  return <div className="space-y-5">
    <div className="flex items-start justify-between gap-3"><div><h1 className="text-xl font-semibold">Memory</h1><p className="mt-1 text-sm text-muted-foreground">Shared knowledge in this Fleet.</p></div>
      <Button variant="outline" size="icon" aria-label="Refresh memories" disabled={loading} onClick={() => setRefresh(n => n + 1)}><RefreshCw className="size-4" /></Button></div>
    {location.state?.memoryNotice ? <p role="status" className="text-sm">{location.state.memoryNotice}</p> : null}
    <form className="flex gap-2" onSubmit={searchSubmit}><Input aria-label="Search memories" placeholder="Search memories" value={search} maxLength={2048} onChange={e => setSearch(e.target.value)} /><Button type="submit"><Search className="size-4" /><span>Search</span></Button></form>
    <ErrorMessage text={error} />
    {loading ? <p className="text-sm text-muted-foreground">Loading memories…</p> : data ? <>
      <p className="text-xs text-muted-foreground">{data.status.entries} memories · {data.status.strategy} · {data.status.pending} pending reviews · {data.status.running} reviewing{!data.status.index_ready ? " · Index not ready" : ""}</p>
      {entries.length ? <ul className="divide-y rounded-md border bg-card">{entries.map(entry => <li key={entry.id} className="p-4">
        <Link className="font-medium text-primary underline-offset-4 hover:underline focus-visible:underline" to={`/memory/${encodeURIComponent(entry.id)}${location.search}`}>{entry.name}</Link>
        <p className="mt-1 break-words text-sm text-muted-foreground">{entry.summary}</p>
        <p className="mt-2 break-all text-xs text-muted-foreground">{entry.type} · {scope(entry)}</p>
      </li>)}</ul> : <p className="rounded-md border p-6 text-sm text-muted-foreground">{query ? "No matching memories." : offset ? "No more memories." : "No memories yet."}</p>}
      {(offset > 0 || data.page.next >= 0) ? <div className="flex items-center justify-between"><Button variant="outline" disabled={!offset} onClick={() => pageTo(Math.max(0, offset - 20))}>Previous</Button><span className="text-xs text-muted-foreground">Page {Math.floor(offset / 20) + 1}</span><Button variant="outline" disabled={data.page.next < 0} onClick={() => pageTo(data.page.next)}>Next</Button></div> : null}
    </> : null}
  </div>;
}

function MemoryDetail({ id }: { id: string }) {
  const navigate = useNavigate(), location = useLocation();
  const [entry, setEntry] = useState<MemoryEntry | null>(null);
  const [draft, setDraft] = useState<MemoryEntry | null>(null);
  const [structured, setStructured] = useState("");
  const [editing, setEditing] = useState(false), [deleting, setDeleting] = useState(false);
  const [busy, setBusy] = useState(false), [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null), [deleteError, setDeleteError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  const alive = useRef(true);
  const last = useRef<{ fingerprint: string; request: MemoryMutation } | null>(null);
  const cancelDelete = useRef<HTMLButtonElement>(null);
  const deleteTrigger = useRef<HTMLButtonElement>(null);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useEffect(() => {
    let active = true; setLoading(true); setError(null);
    readMemory(id).then(value => { if (active) setEntry(value); }).catch(cause => { if (active) setError(message(cause)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [id, refresh]);
  function startEditing() {
    if (!entry) return;
    setDraft(structuredClone(entry)); setStructured(JSON.stringify({ entities: entry.entities ?? [], facts: entry.facts ?? [] }, null, 2));
    setError(null); setNotice(null); setEditing(true);
  }
  async function submit(method: "PUT" | "DELETE") {
    if (!entry || busy) return;
    if (method === "PUT" && draft) {
      for (const [label, value, limit, required] of [["Name", draft.name, 128, true], ["Summary", draft.summary, 512, true], ["Body", draft.body, 32 * 1024, false]] as const) {
        if (required && !value.trim()) { setError(`${label} is required.`); return; }
        const bytes = new TextEncoder().encode(value).length;
        if (bytes > limit) { setError(`${label} is too long (${bytes} bytes; maximum ${limit}). Shorten the text before saving.`); return; }
      }
    }
    let changed = draft;
    if (method === "PUT" && draft && ((entry.entities?.length ?? 0) + (entry.facts?.length ?? 0) > 0)) {
      try {
        const knowledge = JSON.parse(structured);
        if (!knowledge || !Array.isArray(knowledge.entities) || !Array.isArray(knowledge.facts)) throw new Error("Structured knowledge needs entities and facts arrays.");
        changed = { ...draft, entities: knowledge.entities, facts: knowledge.facts };
      } catch (cause) { setError(message(cause)); return; }
    }
    const body = method === "PUT" ? { expected_revision: entry.revision, entry: changed! } : { expected_revision: entry.revision, confirm: entry.id };
    const fingerprint = JSON.stringify({ method, body });
    if (last.current?.fingerprint !== fingerprint) last.current = { fingerprint, request: { ...body, key: `memory-ui-${nanoid()}` } };
    const request = last.current.request;
    setBusy(true); setError(null); setDeleteError(null); setNotice(null);
    try {
      const receipt = await changeMemory(id, method, request);
      if (!alive.current) return;
      if (!receipt.committed) throw new Error(receipt.reason || "Memory change has not committed.");
      const text = `${method === "DELETE" ? "Memory deleted." : "Changes saved."}${!receipt.index_ready ? " Search index is not ready yet." : ""}`;
      if (method === "DELETE") { navigate(`/memory${location.search}`, { state: { memoryNotice: text }, replace: true }); return; }
      setNotice(text); setEditing(false); setDraft(null); last.current = null;
      try { const updated = await readMemory(id); if (alive.current) setEntry(updated); }
      catch (cause) { if (alive.current) setError(`Saved, but the displayed entry could not be refreshed: ${message(cause)}`); }
    } catch (cause) {
      if (!alive.current) return;
      const text = cause instanceof APIError && cause.status === 409 ? `${message(cause)}. Cancel and reload the entry before applying your change again.` : message(cause);
      if (method === "DELETE") setDeleteError(text); else setError(text);
    } finally { if (alive.current) setBusy(false); }
  }
  const hasStructured = Boolean(entry?.entities?.length || entry?.facts?.length);
  return <div className="space-y-5">
    <Button asChild variant="ghost" className="-ml-3"><Link to={`/memory${location.search}`}><ArrowLeft className="size-4" />All memories</Link></Button>
    {notice ? <p role="status" className="rounded-md border p-3 text-sm">{notice}</p> : null}
    <ErrorMessage text={error} />
    {loading ? <p className="text-sm text-muted-foreground">Loading memory…</p> : null}
    {!editing ? <div className="flex flex-wrap items-start justify-between gap-3"><h1 className="min-w-0 break-words text-xl font-semibold">{entry?.name || "Memory entry"}</h1><div className="flex gap-2"><Button variant="outline" aria-label="Refresh memory" disabled={busy || loading} onClick={() => setRefresh(n => n + 1)}><RefreshCw className="size-4" /></Button>{entry ? <><Button variant="outline" disabled={busy || loading || Boolean(error)} onClick={startEditing}>Edit</Button><Button ref={deleteTrigger} variant="destructive" disabled={busy || loading || Boolean(error)} onClick={() => { setDeleteError(null); setDeleting(true); }}>Delete</Button></> : null}</div></div> : null}
    {entry && editing && draft ? <form className="space-y-4" onSubmit={event => { event.preventDefault(); void submit("PUT"); }}>
      <h1 className="text-xl font-semibold">Edit memory</h1>
      <fieldset disabled={busy} className="space-y-4">
        <div className={fieldClass}><label htmlFor="memory-name">Name</label><Input id="memory-name" required maxLength={128} value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} /></div>
        <div className={fieldClass}><label htmlFor="memory-summary">Summary</label><Input id="memory-summary" required maxLength={512} value={draft.summary} onChange={e => setDraft({ ...draft, summary: e.target.value })} /></div>
        <div className={fieldClass}><label htmlFor="memory-type">Type</label><select id="memory-type" className={controlClass} value={draft.type} onChange={e => setDraft({ ...draft, type: e.target.value as MemoryEntry["type"] })}>{["user", "feedback", "project", "reference"].map(type => <option key={type} value={type}>{type}</option>)}</select></div>
        <div className={fieldClass}><label htmlFor="memory-body">Body</label><Textarea id="memory-body" className="min-h-56 font-mono text-sm" value={draft.body} onChange={e => setDraft({ ...draft, body: e.target.value })} /></div>
        {hasStructured ? <details className="rounded-md border p-3"><summary className="cursor-pointer text-sm font-medium">Structured knowledge</summary><p className="my-2 text-xs text-muted-foreground">Update the facts alongside the text when their meaning changes. Keep explicit identities and source references.</p><div className={fieldClass}><label htmlFor="memory-structured">Entities and facts (JSON)</label><Textarea id="memory-structured" className="min-h-48 font-mono text-xs" value={structured} onChange={e => setStructured(e.target.value)} /></div></details> : null}
        <p className="text-xs text-muted-foreground">{userControlNotice}</p>
        <div className="flex gap-2"><Button type="submit">{busy ? "Saving…" : "Save changes"}</Button><Button type="button" variant="outline" onClick={() => { setEditing(false); setDraft(null); setError(null); }}>Cancel editing</Button></div>
      </fieldset>
    </form> : entry ? <>
      <p className="break-words text-sm text-muted-foreground">{entry.summary}</p>
      <p className="break-all text-xs text-muted-foreground">{entry.type} · {scope(entry)} · Revision {entry.revision}</p>
      <div className="whitespace-pre-wrap break-words rounded-md border bg-card p-4 text-sm">{entry.body || "No body text."}</div>
      {hasStructured ? <details className="rounded-md border p-3"><summary className="cursor-pointer text-sm font-medium">Structured knowledge</summary><pre className="mt-3 overflow-x-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify({ entities: entry.entities, facts: entry.facts }, null, 2)}</pre></details> : null}
    </> : null}
    {entry ? <details className="rounded-md border p-3"><summary className="cursor-pointer text-sm font-medium">Sources and metadata</summary><div className="mt-3 space-y-2 break-all text-xs text-muted-foreground"><p>ID: {entry.id}</p><p>Scope: {scope(entry)}</p><p>Created: {new Date(entry.created_at).toLocaleString()}</p><p>Updated: {new Date(entry.updated_at).toLocaleString()}</p>{entry.sources?.length ? <ul className="space-y-2">{entry.sources.map((source, index) => <li key={index}>Fleet {source.fleet_id} · Agent {source.agent_id} · Thread {source.thread_id} · {source.generation_id} · {source.from}–{source.through}</li>)}</ul> : <p>No recorded sources.</p>}</div></details> : null}
    <Dialog open={deleting} onOpenChange={open => { if (!busy) setDeleting(open); }}><DialogContent role="alertdialog" showCloseButton={false} onCloseAutoFocus={event => { event.preventDefault(); deleteTrigger.current?.focus(); }} onOpenAutoFocus={event => { event.preventDefault(); cancelDelete.current?.focus(); }}><DialogHeader><DialogTitle>Delete “{entry?.name}”?</DialogTitle><DialogDescription>This removes the memory and prevents automatic relearning from its recorded sources. Original conversations and external copies remain. {userControlNotice}</DialogDescription></DialogHeader><ErrorMessage text={deleteError} /><DialogFooter><Button variant="outline" ref={cancelDelete} disabled={busy} onClick={() => setDeleting(false)}>Cancel</Button><Button variant="destructive" disabled={busy} onClick={() => void submit("DELETE")}>{busy ? "Deleting…" : "Delete memory"}</Button></DialogFooter></DialogContent></Dialog>
  </div>;
}
