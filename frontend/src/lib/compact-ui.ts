import type { Message } from "@/types";

export const PENDING_COMPACT_LABEL = "Context compacting...";
export const LOCAL_COMPACT_COMMAND_ID = "local-compact-command";
export const LOCAL_COMPACT_PENDING_ID = "local-compact-pending";
export const LOCAL_COMPACT_PENDING_KIND = "compact_pending";

export function isCompactCommandInput(input: string): boolean {
  return input.trim() === "/compact";
}

export function isLocalCompactMessage(
  message: Pick<Message, "id" | "kind">,
): boolean {
  return (
    message.id === LOCAL_COMPACT_COMMAND_ID ||
    message.id === LOCAL_COMPACT_PENDING_ID ||
    message.kind === LOCAL_COMPACT_PENDING_KIND
  );
}
