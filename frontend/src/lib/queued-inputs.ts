import type { MediaRef } from "../types.ts";

export type QueuedInput = {
  id: string;
  messageID?: string;
  input: string;
  kind?: string;
  attachments?: MediaRef[];
};

export type QueuedInputState = {
  items: QueuedInput[];
  nextSeq: number;
};

export function createQueuedInputState(): QueuedInputState {
  return { items: [], nextSeq: 0 };
}

export function enqueueQueuedInput(
  state: QueuedInputState,
  input: string | undefined,
  kind: string | undefined,
  pendingCount: number,
  attachments: MediaRef[] = [],
  messageID?: string,
): QueuedInputState {
  const hasInput = Boolean(input) || attachments.length > 0;
  if (!hasInput) return state;
  const current = state.items;
  let nextSeq = state.nextSeq;
  const makeItem = (existing?: QueuedInput): QueuedInput => {
    const item: QueuedInput = {
      id: existing?.id ?? messageID ?? `queued-${nextSeq++}`,
      input: input ?? "",
      kind,
      attachments:
        attachments.length > 0 ? attachments : (existing?.attachments ?? []),
    };
    const persistedID = messageID ?? existing?.messageID;
    if (persistedID) item.messageID = persistedID;
    return item;
  };

  if (pendingCount > 0) {
    if (current.length > pendingCount) return state;
    if (current.length === pendingCount) {
      const index = pendingCount - 1;
      const existing = current[index];
      const nextItem = makeItem(existing);
      if (
        existing?.input === nextItem.input &&
        existing.kind === nextItem.kind &&
        existing.messageID === nextItem.messageID &&
        sameAttachments(existing.attachments, nextItem.attachments ?? [])
      ) {
        return state;
      }
      const next = [...current];
      next[index] = nextItem;
      return { items: next, nextSeq };
    }
  }

  return { items: [...current, makeItem()], nextSeq };
}

function sameAttachments(a: MediaRef[] | undefined, b: MediaRef[]): boolean {
  const left = a ?? [];
  if (left.length !== b.length) return false;
  return left.every((item, index) => {
    const other = b[index];
    return (
      item.artifact_path === other.artifact_path &&
      item.media_type === other.media_type &&
      item.sha256 === other.sha256
    );
  });
}

export function drainQueuedInputs(
  state: QueuedInputState,
  count: number,
): { state: QueuedInputState; drained: QueuedInput[] } {
  if (count <= 0) return { state, drained: [] };
  const drained = state.items.slice(0, count);
  return {
    state: { ...state, items: state.items.slice(drained.length) },
    drained,
  };
}

export function dropQueuedInputs(
  state: QueuedInputState,
  count: number,
): QueuedInputState {
  if (count <= 0) return state;
  return {
    ...state,
    items: state.items.slice(Math.min(count, state.items.length)),
  };
}
