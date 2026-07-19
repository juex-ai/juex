import type {
  ActiveContextSnapshot,
  BrowserEvent,
  CompactSessionResponse,
  CreateSessionResponse,
  DeleteSessionResponse,
  InterruptResponse,
  SessionShowResponse,
  SessionsListResponse,
  StartTurnResponse,
  TurnStatusResponse,
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
  AgentStatus,
  AddAgentRequest,
  AddAgentResponse,
  DirectoryListing,
  RemovedAgent,
  AgentRuntimeStatusSnapshot,
  FleetAgentStatusEvent,
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

export async function listSessions(): Promise<SessionsListResponse> {
  return jsonOrThrow(await fetch(agentAPIPath("/api/sessions")));
}

export async function createSession(): Promise<CreateSessionResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath("/api/sessions"), { method: "POST" }),
  );
}

export interface SessionMessagePageOptions {
  before?: string;
  limit?: number;
}

export async function getSession(
  id: string,
  opts: SessionMessagePageOptions = {},
): Promise<SessionShowResponse> {
  const params = new URLSearchParams();
  if (opts.before) params.set("before", opts.before);
  if (opts.limit !== undefined) params.set("limit", String(opts.limit));
  const query = params.size ? `?${params.toString()}` : "";
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}${query}`)),
  );
}

export async function deleteSession(id: string): Promise<DeleteSessionResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}`), {
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
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/turns`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt, attachments }),
    }),
  );
}

export async function uploadSessionAttachment(
  id: string,
  file: File,
): Promise<MediaRef> {
  const body = new FormData();
  body.append("file", file, file.name);
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/attachments`), {
      method: "POST",
      body,
    }),
  );
}

export async function interrupt(id: string): Promise<InterruptResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/interrupt`), {
      method: "POST",
    }),
  );
}

export async function compactSession(
  id: string,
  reason = "manual",
): Promise<CompactSessionResponse> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/compact`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason }),
    }),
  );
}

export async function getSessionContext(
  id: string,
): Promise<ActiveContextSnapshot> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/context`)),
  );
}

export async function getTurnStatus(
  id: string,
  turnID: string,
): Promise<TurnStatusResponse> {
  return jsonOrThrow(
    await fetch(
      agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/turns/${encodeURIComponent(turnID)}`),
    ),
  );
}

// SubscribeOptions configures the SSE subscription.
export interface SubscribeOptions {
  since?: string;
  onEvent: (e: BrowserEvent) => void;
  onError?: (err: Event) => void;
}

// subscribeEvents opens an EventSource for the given session and invokes
// onEvent for each parsed BrowserEvent. Returns a function that closes the
// connection. Auto-reconnect is the caller's responsibility.
export function subscribeEvents(
  id: string,
  opts: SubscribeOptions,
): () => void {
  const qs = opts.since ? `?since=${encodeURIComponent(opts.since)}` : "";
  const url = agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/events${qs}`);
  const es = new EventSource(url);
  es.addEventListener("message", (ev) => {
    try {
      const e = JSON.parse((ev as MessageEvent).data) as BrowserEvent;
      opts.onEvent(e);
    } catch {
      /* ignore malformed frames */
    }
  });
  if (opts.onError) {
    es.addEventListener("error", opts.onError);
  }
  return () => es.close();
}

export async function getSessionStatus(
  id: string,
): Promise<AgentRuntimeStatusSnapshot> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/status`)),
  );
}

export function subscribeSessionStatus(
  id: string,
  opts: {
    since?: string;
    onStatus: (status: AgentRuntimeStatusSnapshot) => void;
    onError?: (err: Event) => void;
  },
): () => void {
  const qs = opts.since ? `?since=${encodeURIComponent(opts.since)}` : "";
  const es = new EventSource(
    agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/status/events${qs}`),
  );
  es.addEventListener("message", (event) => {
    try {
      opts.onStatus(
        JSON.parse((event as MessageEvent).data) as AgentRuntimeStatusSnapshot,
      );
    } catch {
      /* ignore malformed frames */
    }
  });
  if (opts.onError) es.addEventListener("error", opts.onError);
  return () => es.close();
}

export async function getFileTree(signal?: AbortSignal): Promise<FileNode> {
  return jsonOrThrow(await fetch(agentAPIPath("/api/files/tree"), { signal }));
}

export async function getSessionScratchpad(
  id: string,
  signal?: AbortSignal,
): Promise<FileNode> {
  return jsonOrThrow(
    await fetch(agentAPIPath(`/api/sessions/${encodeURIComponent(id)}/scratchpad`), { signal }),
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

export function getFileRawURL(path: string): string {
  return agentAPIPath(`/api/files/raw?path=${encodeURIComponent(path)}`);
}

export function getMediaURL(path: string): string {
  return agentAPIPath(`/api/media?path=${encodeURIComponent(path)}`);
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

export function subscribeFleetEvents(opts: {
  onEvent: (event: FleetAgentStatusEvent) => void;
  onError?: (err: Event) => void;
}): () => void {
  const es = new EventSource("/api/fleet/events");
  es.addEventListener("message", (event) => {
    try {
      const parsed = JSON.parse((event as MessageEvent).data) as FleetAgentStatusEvent;
      if (parsed.type === "agent.status") opts.onEvent(parsed);
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

export async function runAgentAction(
  id: string,
  action: "start" | "stop" | "restart",
): Promise<AgentStatus> {
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
