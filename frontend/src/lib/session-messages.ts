import type { SessionShowResponse } from "@/types";

export function mergeOlderSessionPage(
  current: SessionShowResponse | null,
  older: SessionShowResponse,
): SessionShowResponse {
  if (!current) return older;
  const currentMessages = current.messages ?? [];
  const currentIDs = new Set(
    currentMessages.flatMap((message) => (message.id ? [message.id] : [])),
  );
  const olderMessages = (older.messages ?? []).filter(
    (message) => !message.id || !currentIDs.has(message.id),
  );
  return {
    ...current,
    messages: [...olderMessages, ...currentMessages],
    has_more_before: older.has_more_before,
    oldest_message_id: older.oldest_message_id,
  };
}
