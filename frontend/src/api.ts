import type {
  BusEvent,
  CreateSessionResponse,
  InterruptResponse,
  SessionShowResponse,
  SessionsListResponse,
  StartTurnResponse,
  TurnStatusResponse,
} from "./types";

const BASE = "";  // same-origin; Vite dev proxy handles /api → :8080

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
      if (body && typeof body.message === "string") message = body.message;
    } catch {
      /* response wasn't JSON; keep statusText */
    }
    throw new APIError(r.status, message);
  }
  return (await r.json()) as T;
}

export async function listSessions(): Promise<SessionsListResponse> {
  return jsonOrThrow(await fetch(`${BASE}/api/sessions`));
}

export async function createSession(): Promise<CreateSessionResponse> {
  return jsonOrThrow(
    await fetch(`${BASE}/api/sessions`, { method: "POST" }),
  );
}

export async function getSession(id: string): Promise<SessionShowResponse> {
  return jsonOrThrow(
    await fetch(`${BASE}/api/sessions/${encodeURIComponent(id)}`),
  );
}

export async function startTurn(
  id: string,
  prompt: string,
): Promise<StartTurnResponse> {
  return jsonOrThrow(
    await fetch(`${BASE}/api/sessions/${encodeURIComponent(id)}/turns`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt }),
    }),
  );
}

export async function interrupt(id: string): Promise<InterruptResponse> {
  return jsonOrThrow(
    await fetch(`${BASE}/api/sessions/${encodeURIComponent(id)}/interrupt`, {
      method: "POST",
    }),
  );
}

export async function getTurnStatus(
  id: string,
  turnID: string,
): Promise<TurnStatusResponse> {
  return jsonOrThrow(
    await fetch(
      `${BASE}/api/sessions/${encodeURIComponent(id)}/turns/${encodeURIComponent(turnID)}`,
    ),
  );
}

// SubscribeOptions configures the SSE subscription.
export interface SubscribeOptions {
  since?: string;
  onEvent: (e: BusEvent) => void;
  onError?: (err: Event) => void;
}

// subscribeEvents opens an EventSource for the given session and invokes
// onEvent for each parsed BusEvent. Returns a function that closes the
// connection. Auto-reconnect is the caller's responsibility.
export function subscribeEvents(
  id: string,
  opts: SubscribeOptions,
): () => void {
  const qs = opts.since ? `?since=${encodeURIComponent(opts.since)}` : "";
  const url = `${BASE}/api/sessions/${encodeURIComponent(id)}/events${qs}`;
  const es = new EventSource(url);
  es.addEventListener("message", (ev) => {
    try {
      const e = JSON.parse((ev as MessageEvent).data) as BusEvent;
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

export { APIError };
