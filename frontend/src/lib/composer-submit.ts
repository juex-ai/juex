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
}: {
  turnActive: boolean;
  compactActive: boolean;
  text: string;
}): ComposerSubmitAction {
  const hasText = text.trim().length > 0;
  if (!hasText) {
    if (turnActive) return "stop";
    if (compactActive) return "compacting";
    return "empty";
  }
  return turnActive || compactActive ? "queue" : "send";
}
