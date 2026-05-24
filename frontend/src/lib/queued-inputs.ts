export type QueuedInput = {
  id: string;
  input: string;
  kind?: string;
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
): QueuedInputState {
  if (!input) return state;
  const current = state.items;
  let nextSeq = state.nextSeq;
  const makeItem = (existing?: QueuedInput): QueuedInput => ({
    id: existing?.id ?? `queued-${nextSeq++}`,
    input,
    kind,
  });

  if (pendingCount > 0) {
    if (current.length > pendingCount) return state;
    if (current.length === pendingCount) {
      const index = pendingCount - 1;
      const existing = current[index];
      if (existing?.input === input && existing.kind === kind) return state;
      const next = [...current];
      next[index] = makeItem(existing);
      return { items: next, nextSeq };
    }
  }

  return { items: [...current, makeItem()], nextSeq };
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
