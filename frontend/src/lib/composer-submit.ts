export type ComposerSubmitAction =
  | "empty"
  | "compacting"
  | "stop"
  | "send"
  | "queue";

export const COMPACTING_SUBMIT_HINT = "Context is compacting";

export function composerSubmitAction({
  turnActive,
  compactActive,
  text,
  attachmentCount = 0,
}: {
  turnActive: boolean;
  compactActive: boolean;
  text: string;
  attachmentCount?: number;
}): ComposerSubmitAction {
  const hasInput = text.trim().length > 0 || attachmentCount > 0;
  if (!hasInput) {
    if (turnActive) return "stop";
    if (compactActive) return "compacting";
    return "empty";
  }
  return turnActive || compactActive ? "queue" : "send";
}
