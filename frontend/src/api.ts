import type {
  ActiveContextSnapshot,
  BrowserEvent,
  CompactThreadResponse,
  CreateThreadResponse,
  DeleteThreadResponse,
  InterruptResponse,
  Message,
  ThreadInfo,
  ThreadShowResponse,
  ThreadsListResponse,
  StartTurnResponse,
  MediaRef,
  FileContentResponse,
  FileNode,
  ObservableCreateRequest,
  ObservableDetailResponse,
  ObservableObservationsResponse,
  ObservationRecord,
  ObservableStatus,
  ObservablesListResponse,
  RuntimeStatusResponse,
  AgentConfig,
  AgentConfigUpdateResponse,
  AgentActionResult,
  AgentStatus,
  AddAgentRequest,
  AddAgentResponse,
  CreateDirectoryRequest,
  DirectoryEntry,
  DirectoryListing,
  RemovedAgent,
  AgentRuntimeStatusSnapshot,
  FleetEvent,
  AgentResourceEvent,
  FleetStatus,
} from "./types";
import { agentBasePath } from "./lib/fleet-routes.ts";

function agentAPIPath(path: string): string {
  const pathname = typeof window === "undefined" ? "" : window.location.pathname;
  return `${agentBasePath(pathname)}${path}`;
}

class APIError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function jsonOrThrow<T>(r: Response): Promise<T> {
  if (!r.ok) {
    let message = r.statusText || `HTTP ${r.status}`;
    try {
      const body = await r.json();
      if (body && typeof body.message === "string") {
        message = body.message;
      } else if (
        body &&
        typeof body.error === "object" &&
        body.error !== null &&
        typeof body.error.message === "string"
      ) {
        message = body.error.message;
      }
    } catch {
      /* response wasn't JSON; keep statusText */
    }
    throw new APIError(r.status, message);
  }
  return (await r.json()) as T;
}

export async function listThreads(): Promise<ThreadsListResponse> {
  return jsonOrThrow(await fetch(agentAPIPath("/api/threads")));
}

export async function createThread(alias?: string): Promise<CreateThreadResponse> {
  const raw = await jsonOrThrow<RawThreadInfo>(
    await fetch(agentAPIPath("/api/threads"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ alias: alias?.trim() || undefined }),
    }),
  );
  return normalizeThreadInfo(raw);
}

export interface ThreadMessagePageOptions {
  before?: string;
  limit?: number;
}

export async function getThread(
  id: string,
  opts: ThreadMessagePageOptions = {},
): Promise<ThreadShowResponse> {
  const params = new URLSearchParams();
  if (opts.before) params.set("before", opts.before);
  if (opts.limit !== undefined) params.set("limit", String(opts.limit));
  const query = params.size ? `?${params.toString()}` : "";
  const raw = await jsonOrThrow<RawThreadShowResponse>(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}${query}`)),
  );
  return normalizeThreadShow(raw);
}

export async function archiveThread(id: string): Promise<void> {
  await jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/archive`), {
      method: "POST",
    }),
  );
}

export async function unarchiveThread(id: string): Promise<ThreadInfo> {
  const raw = await jsonOrThrow<RawThreadInfo>(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/unarchive`), {
      method: "POST",
    }),
  );
  return normalizeThreadInfo(raw);
}

export async function deleteThread(id: string): Promise<DeleteThreadResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}`), {
      method: "DELETE",
    }),
  );
}

export async function startTurn(
  id: string,
  prompt: string,
  attachments: MediaRef[] = [],
): Promise<StartTurnResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/inputs`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt, attachments }),
    }),
  );
}

export async function uploadThreadAttachment(
  id: string,
  file: File,
): Promise<MediaRef> {
  const body = new FormData();
  body.append("file", file, file.name);
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/attachments`), {
      method: "POST",
      body,
    }),
  );
}

export async function interrupt(id: string): Promise<InterruptResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/stop`), {
      method: "POST",
    }),
  );
}

interface RawThreadInfo {
  thread_id: string;
  alias: string;
  parent_thread_id?: string;
  dir: string;
  created_at: string;
  last_activity_at: string;
  archived_at?: string;
  retention_state: ThreadInfo["retention_state"];
  execution_state?: ThreadInfo["execution_state"];
  revision: number;
  generation_id: string;
  turn_count: number;
  pending_input_count: number;
  token_usage: ThreadInfo["token_usage"];
  context_usage?: ThreadInfo["context_usage"];
}

interface RawThreadActivity {
  type: "context.compacted" | "context.renewed" | string;
  at: string;
  from_generation_id?: string;
  to_generation_id?: string;
  summary?: Message;
  automatic?: boolean;
}

interface RawThreadTimelineItem {
  type: "message" | "activity";
  seq: number;
  at: string;
  message?: Message;
  activity?: RawThreadActivity;
}

interface RawThreadShowResponse extends RawThreadInfo {
  items: RawThreadTimelineItem[];
  event_cursor?: string;
  has_more_before?: boolean;
  previous_cursor?: string;
  goal?: ThreadShowResponse["goal"];
  notes?: ThreadShowResponse["notes"];
}

function normalizeThreadInfo(raw: RawThreadInfo): ThreadInfo {
  return {
    id: raw.thread_id,
    alias: raw.alias,
    parent_thread_id: raw.parent_thread_id,
    dir: raw.dir,
    retention_state: raw.retention_state,
    execution_state: raw.execution_state,
    created_at: raw.created_at,
    last_active_at: raw.last_activity_at || raw.created_at,
    revision: raw.revision,
    generation_id: raw.generation_id,
    turns: raw.turn_count,
    pending_input_count: raw.pending_input_count,
    token_usage: raw.token_usage,
    context_usage: raw.context_usage,
  };
}

function normalizeThreadShow(raw: RawThreadShowResponse): ThreadShowResponse {
  return {
    ...normalizeThreadInfo(raw),
    messages: (raw.items ?? []).flatMap((item) => timelineMessage(item)),
    event_cursor: raw.event_cursor ?? "",
    has_more_before: raw.has_more_before,
    oldest_message_id: raw.previous_cursor,
    goal: raw.goal,
    notes: raw.notes,
  };
}

function timelineMessage(item: RawThreadTimelineItem): Message[] {
  if (item.type === "message" && item.message) {
    return [{ ...item.message, created_at: item.message.created_at ?? item.at }];
  }
  const activity = item.activity;
  if (!activity) return [];
  if (activity.type === "context.compacted") {
    const summary = activity.summary;
    return [{
      ...(summary ?? { role: "assistant", blocks: [] }),
      id: `activity-${item.seq}`,
      created_at: activity.at || item.at,
      kind: "compact",
    }];
  }
  if (activity.type === "context.renewed") {
    return [{
      id: `activity-${item.seq}`,
      created_at: activity.at || item.at,
      role: "system",
      kind: "context_renewed",
      blocks: [{
        type: "text",
        text: `Context renewed${activity.to_generation_id ? ` · ${activity.to_generation_id}` : ""}`,
      }],
    }];
  }
  return [];
}

export async function compactThread(
  id: string,
  reason = "manual",
): Promise<CompactThreadResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/compact`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason }),
    }),
  );
}

export async function getThreadContext(
  id: string,
): Promise<ActiveContextSnapshot> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/context`)),
  );
}

// SubscribeOptions configures the SSE subscription.
export interface SubscribeOptions {
  since?: string;
  onEvent: (e: BrowserEvent) => void;
  onOpen?: () => void;
  onError?: (err: Event) => void;
}

// subscribeEvents opens an EventSource for the given thread and invokes
// onEvent for each parsed BrowserEvent. Returns a function that closes the
// connection. EventSource reconnects automatically with the last durable SSE
// event ID; transient frames deliberately do not advance that cursor.
//
// An empty cursor means the journal was empty when the transcript snapshot was
// taken: there is no event to resume after, but whatever was committed before
// the stream attached is still needed. That is requested through a separate
// `replay` parameter rather than a reserved `since` value, so the cursor stays
// an opaque event ID and a cursor the client merely lost can never be read as
// "replay everything".
export function subscribeEvents(
  id: string,
  opts: SubscribeOptions,
): () => void {
  let qs = "";
  if (opts.since) {
    qs = `?since=${encodeURIComponent(opts.since)}`;
  } else if (opts.since === "") {
    qs = "?replay=journal-start";
  }
  const url = agentAPIPath(`/api/threads/${encodeURIComponent(id)}/events${qs}`);
  const es = new EventSource(url);
  es.addEventListener("message", (ev) => {
    try {
      const e = JSON.parse((ev as MessageEvent).data) as BrowserEvent;
      opts.onEvent(e);
    } catch {
      /* ignore malformed frames */
    }
  });
  if (opts.onOpen) {
    es.addEventListener("open", opts.onOpen);
  }
  if (opts.onError) {
    es.addEventListener("error", opts.onError);
  }
  return () => es.close();
}

export async function getThreadStatus(
  id: string,
): Promise<AgentRuntimeStatusSnapshot> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/status`)),
  );
}

export async function getFileTree(signal?: AbortSignal): Promise<FileNode> {
  return jsonOrThrow(await fetch(agentAPIPath("/api/files/tree"), { signal }));
}

export async function getThreadScratchpad(
  id: string,
  signal?: AbortSignal,
): Promise<FileNode> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/threads/${encodeURIComponent(id)}/scratchpad`), { signal }),
  );
}

export async function getFileContent(
  path: string,
  signal?: AbortSignal,
): Promise<FileContentResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/files/content?path=${encodeURIComponent(path)}`), { signal }),
  );
}

export async function getArtifactContent(
  path: string,
  signal?: AbortSignal,
): Promise<FileContentResponse> {
  return jsonOrThrow(
    await fetch(
      agentAPIPath(
        `/api/files/content?root=artifact&path=${encodeURIComponent(path)}`,
      ),
      { signal },
    ),
  );
}

export async function getMediaMetadata(
  path: string,
  signal?: AbortSignal,
): Promise<MediaRef> {
  const response = await fetch(getMediaURL(path, "workspace"), {
    method: "HEAD",
    signal,
  });
  if (!response.ok) {
    throw new APIError(
      response.status,
      response.statusText || `HTTP ${response.status}`,
    );
  }

  const contentLengthHeader = response.headers.get("Content-Length");
  const contentLength =
    contentLengthHeader === null ? Number.NaN : Number(contentLengthHeader);
  return {
    artifact_path: path,
    media_type: response.headers.get("Content-Type") || undefined,
    ...(Number.isFinite(contentLength) && contentLength >= 0
      ? { original_bytes: contentLength }
      : {}),
  };
}

export function getFileRawURL(path: string): string {
  return agentAPIPath(`/api/files/raw?path=${encodeURIComponent(path)}`);
}

export type MediaRoot = "artifact" | "workspace";

export function getMediaURL(path: string, root: MediaRoot): string {
  return agentAPIPath(
    `/api/media?root=${encodeURIComponent(root)}&path=${encodeURIComponent(path)}`,
  );
}

export async function getRuntimeStatus(): Promise<RuntimeStatusResponse> {
  return jsonOrThrow(await fetch(agentAPIPath("/api/runtime")));
}

export async function listObservables(): Promise<ObservablesListResponse> {
  return jsonOrThrow(await fetch(agentAPIPath("/api/observables")));
}

export async function createObservable(
  input: ObservableCreateRequest,
): Promise<ObservableStatus> {
  return jsonOrThrow(
    await fetch(agentAPIPath("/api/observables"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  );
}

export async function getObservable(
  id: string,
): Promise<ObservableDetailResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/observables/${encodeURIComponent(id)}`)),
  );
}

export async function startObservable(id: string): Promise<ObservableStatus> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/observables/${encodeURIComponent(id)}/start`), {
      method: "POST",
    }),
  );
}

export async function stopObservable(id: string): Promise<ObservableStatus> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/observables/${encodeURIComponent(id)}/stop`), {
      method: "POST",
    }),
  );
}

export async function runObservable(id: string): Promise<ObservationRecord> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/observables/${encodeURIComponent(id)}/run`), {
      method: "POST",
    }),
  );
}

export async function deleteObservable(
  id: string,
): Promise<{ deleted: string }> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/observables/${encodeURIComponent(id)}`), {
      method: "DELETE",
    }),
  );
}

export async function listObservableObservations(
  id: string,
  limit = 50,
): Promise<ObservableObservationsResponse> {
  return jsonOrThrow(
    await fetch(
      agentAPIPath(`/api/observables/${encodeURIComponent(id)}/observations?limit=${encodeURIComponent(String(limit))}`),
    ),
  );
}

export async function listAgents(): Promise<AgentStatus[]> {
  return jsonOrThrow(await fetch("/api/agents"));
}

export async function getFleetStatus(): Promise<FleetStatus> {
  return jsonOrThrow(await fetch("/api/fleet/status"));
}

export function subscribeFleetEvents(opts: {
  onEvent: (event: FleetEvent) => void;
  onError?: (err: Event) => void;
}): () => void {
  const es = new EventSource("/api/fleet/events");
  es.addEventListener("message", (event) => {
    try {
      const parsed = JSON.parse((event as MessageEvent).data) as FleetEvent;
      if (
        parsed.type === "agent.status" ||
        parsed.type === "agent.process" ||
        parsed.type === "fleet.roster" ||
        parsed.type === "fleet.roster.unavailable" ||
        parsed.type === "fleet.status"
      ) {
        opts.onEvent(parsed);
      }
    } catch {
      /* ignore malformed frames */
    }
  });
  if (opts.onError) es.addEventListener("error", opts.onError);
  return () => es.close();
}

export function subscribeAgentResourceEvents(opts: {
  onEvent: (event: AgentResourceEvent) => void;
  onError?: (err: Event) => void;
}): () => void {
  const es = new EventSource(agentAPIPath("/api/resource-events"));
  es.addEventListener("message", (event) => {
    try {
      const parsed = JSON.parse((event as MessageEvent).data) as AgentResourceEvent;
      if (parsed.type === "resource.changed" && Array.isArray(parsed.resources)) {
        opts.onEvent(parsed);
      }
    } catch {
      /* ignore malformed frames */
    }
  });
  if (opts.onError) es.addEventListener("error", opts.onError);
  return () => es.close();
}

export async function addAgent(
  input: AddAgentRequest,
): Promise<AddAgentResponse> {
  return jsonOrThrow(
    await fetch("/api/agents", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  );
}

export async function listDirectories(
  path?: string,
  showHidden = false,
): Promise<DirectoryListing> {
  const params = new URLSearchParams();
  if (path) params.set("path", path);
  if (showHidden) params.set("show_hidden", "true");
  const query = params.toString();
  return jsonOrThrow(
    await fetch(`/api/fs/dirs${query ? `?${query}` : ""}`),
  );
}

export async function createDirectory(
  input: CreateDirectoryRequest,
): Promise<DirectoryEntry> {
  return jsonOrThrow(
    await fetch("/api/fs/dirs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),
  );
}

export async function runAgentAction(
  id: string,
  action: "start" | "stop" | "restart",
): Promise<AgentActionResult> {
  return jsonOrThrow(
    await fetch(`/api/agents/${encodeURIComponent(id)}/${action}`, {
      method: "POST",
    }),
  );
}

export async function setAgentEnabled(
  id: string,
  enabled: boolean,
): Promise<AgentStatus> {
  return jsonOrThrow(
    await fetch(
      `/api/agents/${encodeURIComponent(id)}/${enabled ? "enable" : "disable"}`,
      { method: "POST" },
    ),
  );
}

export async function removeAgent(
  id: string,
  confirm: string,
): Promise<RemovedAgent> {
  return jsonOrThrow(
    await fetch(`/api/agents/${encodeURIComponent(id)}`, {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ confirm }),
    }),
  );
}

export async function getAgentLogs(
  id: string,
  lines = 200,
): Promise<string> {
  const response = await jsonOrThrow<{ content: string }>(
    await fetch(
      `/api/agents/${encodeURIComponent(id)}/logs?lines=${encodeURIComponent(String(lines))}`,
    ),
  );
  return response.content;
}

export async function getAgentConfig(id: string): Promise<AgentConfig> {
  return jsonOrThrow(
    await fetch(`/api/agents/${encodeURIComponent(id)}/config`),
  );
}

export async function updateAgentConfig(
  id: string,
  content: string,
): Promise<AgentConfigUpdateResponse> {
  return jsonOrThrow(
    await fetch(`/api/agents/${encodeURIComponent(id)}/config`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content }),
    }),
  );
}

export { APIError };
