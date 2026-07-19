import type { AgentRuntimeStatusSnapshot } from "../types.ts";

export type ComposerSubmitAction =
  | "empty"
  | "queue-full"
  | "stop"
  | "send"
  | "queue";

export const QUEUE_FULL_SUBMIT_HINT = "Pending input queue is full";

export function composerSubmitAction({
  status,
  turnActiveFallback = false,
  text,
  attachmentCount = 0,
}: {
  status?: AgentRuntimeStatusSnapshot;
  turnActiveFallback?: boolean;
  text: string;
  attachmentCount?: number;
}): ComposerSubmitAction {
  const hasInput = text.trim().length > 0 || attachmentCount > 0;
  const turnActive =
    status?.turn?.state === "admitted" ||
    status?.turn?.state === "active" ||
    (!status && turnActiveFallback);
  if (!hasInput) return turnActive ? "stop" : "empty";
  if (status && !status.session.can_accept_input) return "queue-full";
  return turnActive ? "queue" : "send";
}
