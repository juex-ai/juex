import type { ThreadShowResponse } from "@/types";

const canonicalMessageIDPattern =
  /^msg-(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})-[0-9a-f]{8}$/;

export function messageCreatedAtFromID(
  id: string | undefined,
): string | undefined {
  const match = id?.match(canonicalMessageIDPattern);
  if (!match) return undefined;
  const [, year, month, day, hour, minute, second] = match;
  const createdAt = `${year}-${month}-${day}T${hour}:${minute}:${second}Z`;
  const parsed = new Date(createdAt);
  if (!Number.isFinite(parsed.valueOf())) return undefined;
  return parsed.toISOString().replace(".000Z", "Z") === createdAt
    ? createdAt
    : undefined;
}

export function mergeOlderThreadPage(
  current: ThreadShowResponse | null,
  older: ThreadShowResponse,
): ThreadShowResponse {
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
