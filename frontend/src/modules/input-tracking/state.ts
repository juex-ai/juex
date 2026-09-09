import type { BrowserEvent, InputCheck, InputStatus, InputStatusPage } from "../../types.ts";

export interface LiveInputStatus {
  messages: Record<string, InputStatus>;
  checks: Record<string, InputCheck>;
}
const key = (scope: string, id: string) => `${scope}\0${id}`;
export function createLiveInputStatus(): LiveInputStatus { return { messages: {}, checks: {} }; }

export function projectInputStatusEvent(state: LiveInputStatus, event: BrowserEvent): LiveInputStatus {
  if (event.type === "input.tracked") {
    return { ...state, messages: { ...state.messages, [event.payload.message_id]: event.payload } };
  }
  if (event.type !== "input.checked") return state;
  const checks = { ...state.checks };
  for (const id of event.payload.input_ids) {
    checks[key(event.payload.scope_id, id)] ??= event.payload;
  }
  return { ...state, checks };
}

// Only an enabled server page admits annotations. A check event can arrive
// before its message/page, so retain the explicit evidence until they meet.
export function mergeInputStatus(page: InputStatusPage | undefined, live: LiveInputStatus): InputStatusPage | undefined {
  if (!page || page.error) return undefined;
  const messages = { ...live.messages, ...page.messages };
  for (const [id, status] of Object.entries(messages)) {
    const check = live.checks[key(status.scope_id, status.input_id)];
    if (check && !status.checked_at) messages[id] = {
      ...status, checked_at: check.checked_at, check_message_id: check.message_id, tool_use_id: check.tool_use_id,
    };
  }
  return { ...page, messages };
}

export function inputStatusLabel(status: InputStatus, scopeID: string): string {
  if (status.checked_at) return "Checked by Agent";
  return status.scope_id === scopeID ? "Not checked" : "Tracking ended";
}

export function reconcileInputStatus(page: InputStatusPage | undefined, live: LiveInputStatus): LiveInputStatus {
  if (!page) return createLiveInputStatus();
  if (page.error) return live;
  const messages = { ...live.messages };
  const checks = { ...live.checks };
  for (const status of Object.values(page.messages)) {
    delete messages[status.message_id];
    if (status.checked_at) delete checks[key(status.scope_id, status.input_id)];
  }
  return { messages, checks };
}
