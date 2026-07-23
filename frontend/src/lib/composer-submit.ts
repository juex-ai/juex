import type { AgentRuntimeStatusSnapshot } from "../types.ts";

export type ComposerSubmitAction =
  | "loading"
  | "empty"
  | "queue-full"
  | "stop"
  | "send"
  | "queue";

export const QUEUE_FULL_SUBMIT_HINT = "Pending input queue is full";

export function settleSubmittedComposerText(
  currentText: string,
  submittedText: string,
): string {
  return currentText === submittedText ? "" : currentText;
}

export function composerErrorMessage({
  status,
  localError,
}: {
  status?: AgentRuntimeStatusSnapshot;
  localError?: string;
}): string | undefined {
  return status?.last_error?.message || localError;
}

export function composerSubmitAction({
  status,
  text,
  attachmentCount = 0,
}: {
  status?: AgentRuntimeStatusSnapshot;
  text: string;
  attachmentCount?: number;
}): ComposerSubmitAction {
  if (!status) return "loading";
  const hasInput = text.trim().length > 0 || attachmentCount > 0;
  const working = status.session.working;
  const canInterrupt = status?.turn?.can_interrupt !== false;
  if (!hasInput) return working && canInterrupt ? "stop" : "empty";
  if (status && !status.session.can_accept_input) return "queue-full";
  return working ? "queue" : "send";
}
