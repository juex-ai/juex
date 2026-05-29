export type ComposerSubmitAction = "empty" | "stop" | "send" | "queue";

export function composerSubmitAction({
  busy,
  text,
}: {
  busy: boolean;
  text: string;
}): ComposerSubmitAction {
  const hasText = text.trim().length > 0;
  if (!hasText) return busy ? "stop" : "empty";
  return busy ? "queue" : "send";
}
