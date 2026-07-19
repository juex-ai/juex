import { assistantBlocksFromEventPayload } from "./assistant-blocks.ts";
import {
  isLocalCompactMessage,
  LOCAL_COMPACT_COMMAND_ID,
  LOCAL_COMPACT_PENDING_ID,
  LOCAL_COMPACT_PENDING_KIND,
  PENDING_COMPACT_LABEL,
} from "./compact-ui.ts";
import {
  applyToolOutputDeltaToMessages,
  applyToolRequestedToMessages,
  applyToolResultToMessages,
} from "./live-tool-events.ts";
import {
  createQueuedInputState,
  drainQueuedInputs as drainQueuedInputState,
  dropQueuedInputs as dropQueuedInputState,
  enqueueQueuedInput as enqueueQueuedInputState,
  type QueuedInput,
  type QueuedInputState,
} from "./queued-inputs.ts";
import type {
  BrowserEvent,
  Block,
  ContextUsage,
  MediaRef,
  Message,
  SessionTurnStatus,
  TokenUsage,
  TurnStatusResponse,
} from "../types.ts";

export type LiveSessionStatus =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "pending"; count: number }
  | { kind: "tool"; name: string }
  | { kind: "done" }
  | { kind: "error"; detail?: string };

export type LiveSessionProjection = {
  messages: Message[];
  tokenUsage: TokenUsage | null;
  contextUsage: ContextUsage | null;
  queuedInput: QueuedInputState;
  // Empty slots reserve announced drain positions missing from local queue state.
  drainingQueuedInputs: Array<QueuedInput | undefined>;
  turnActive: boolean;
  compactActive: boolean;
  compactCommandInputs: Record<string, string>;
  status: LiveSessionStatus;
};

export type LiveSessionProjectionEffect =
  | { type: "refresh"; preserveLiveMessages?: boolean }
  | { type: "scheduleIdleStatus" };

export type LiveSessionProjectionResult = {
  state: LiveSessionProjection;
  effects: LiveSessionProjectionEffect[];
};

export function createLiveSessionProjection(): LiveSessionProjection {
  return {
    messages: [],
    tokenUsage: null,
    contextUsage: null,
    queuedInput: createQueuedInputState(),
    drainingQueuedInputs: [],
    turnActive: false,
    compactActive: false,
    compactCommandInputs: {},
    status: { kind: "idle" },
  };
}

export function resetLiveSessionProjection(opts?: {
  activeTurnID?: string;
}): LiveSessionProjection {
  return {
    ...createLiveSessionProjection(),
    turnActive: Boolean(opts?.activeTurnID),
    status: opts?.activeTurnID ? { kind: "running" } : { kind: "idle" },
  };
}

export function clearLiveSessionTranscript(
  state: LiveSessionProjection,
): LiveSessionProjection {
  return { ...state, messages: [], tokenUsage: null, contextUsage: null };
}

export function clearQueuedInputs(
  state: LiveSessionProjection,
): LiveSessionProjection {
  if (
    state.queuedInput.items.length === 0 &&
    state.drainingQueuedInputs.length === 0
  ) {
    return state;
  }
  return {
    ...state,
    queuedInput: createQueuedInputState(),
    drainingQueuedInputs: [],
  };
}

export function clearLocalCompactMessages(
  state: LiveSessionProjection,
): LiveSessionProjection {
  return {
    ...state,
    messages: state.messages.filter((message) => !isLocalCompactMessage(message)),
  };
}

export function markProjectionDone(
  state: LiveSessionProjection,
): LiveSessionProjection {
  return { ...state, status: { kind: "done" } };
}

export function markProjectionIdle(
  state: LiveSessionProjection,
): LiveSessionProjection {
  return { ...state, status: { kind: "idle" } };
}

export function markProjectionError(
  state: LiveSessionProjection,
  detail?: string,
): LiveSessionProjection {
  return { ...state, status: { kind: "error", detail } };
}

export function projectCompactCommand(
  state: LiveSessionProjection,
  messageID: string | undefined,
  input: string,
): LiveSessionProjection {
  if (!messageID) return state;
  return {
    ...state,
    compactCommandInputs: { ...state.compactCommandInputs, [messageID]: input },
  };
}

export function projectCommandResult(
  state: LiveSessionProjection,
  input: string,
  output: string,
): LiveSessionProjection {
  return {
    ...state,
    messages: [
      ...state.messages,
      {
        role: "user",
        kind: "slash_command",
        blocks: [{ type: "text", text: input }],
      },
      {
        role: "assistant",
        kind: "slash_command",
        blocks: [{ type: "text", text: output }],
      },
    ],
  };
}

export function projectOptimisticTurn(
  state: LiveSessionProjection,
  turnID: string | undefined,
  input: string | undefined,
  kind?: string,
  attachments: MediaRef[] = [],
): LiveSessionProjection {
  return {
    ...state,
    messages: appendLiveTurnToMessages(
      state.messages,
      turnID,
      input,
      kind,
      "optimistic",
      attachments,
    ),
    turnActive: true,
    status: { kind: "running" },
  };
}

export function projectQueuedInput(
  state: LiveSessionProjection,
  input: string | undefined,
  kind: string | undefined,
  pendingCount: number,
  attachments: MediaRef[] = [],
): LiveSessionProjection {
  return {
    ...state,
    queuedInput: enqueueQueuedInputState(
      state.queuedInput,
      input,
      kind,
      pendingCount,
      attachments,
    ),
    turnActive: true,
    status: { kind: "pending", count: pendingCount },
  };
}

export function projectPendingCompact(
  state: LiveSessionProjection,
  commandInput?: string,
): LiveSessionProjection {
  let messages = state.messages;
  if (
    commandInput &&
    !messages.some((message) => message.id === LOCAL_COMPACT_COMMAND_ID)
  ) {
    messages = [
      ...messages,
      {
        id: LOCAL_COMPACT_COMMAND_ID,
        role: "user",
        kind: "slash_command",
        blocks: [{ type: "text", text: commandInput }],
      },
    ];
  }
  if (!messages.some((message) => message.id === LOCAL_COMPACT_PENDING_ID)) {
    messages = [
      ...messages,
      {
        id: LOCAL_COMPACT_PENDING_ID,
        role: "user",
        kind: LOCAL_COMPACT_PENDING_KIND,
        pending: true,
        blocks: [{ type: "text", text: PENDING_COMPACT_LABEL }],
      },
    ];
  }
  return {
    ...state,
    messages,
    compactActive: true,
    status: { kind: "running" },
  };
}

export function projectLiveSessionEvent(
  state: LiveSessionProjection,
  event: BrowserEvent,
): LiveSessionProjectionResult {
  let next = state;
  const effects: LiveSessionProjectionEffect[] = [];

  switch (event.type) {
    case "turn.started": {
      const alreadyProjected = Boolean(
        event.turn_id &&
          next.messages.some(
            (message) =>
              message.role === "user" && message.turn_id === event.turn_id,
          ),
      );
      const consumed = alreadyProjected
        ? { state: next }
        : consumeQueuedInput(next, event.payload.input, event.payload.kind);
      next = consumed.state;
      next = {
        ...next,
        messages: appendLiveTurnToMessages(
          next.messages,
          event.turn_id,
          event.payload.input,
          event.payload.kind,
          "event",
          consumed.item?.attachments,
        ),
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    }
    case "llm.requested":
      next = { ...next, turnActive: true, status: { kind: "running" } };
      break;
    case "llm.output_delta":
      next = {
        ...applyAssistantOutputDelta(next, event),
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    case "llm.responded":
      next = {
        ...applyAssistantResponse(next, event),
        tokenUsage: tokenUsageFromEvent(event) ?? next.tokenUsage,
        contextUsage: event.payload.context_usage ?? next.contextUsage,
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    case "llm.retry":
      next = event.payload.purpose === "compaction"
        ? { ...next, compactActive: true, status: { kind: "running" } }
        : {
            ...resetPendingAssistantOutput(next, event.turn_id, event.payload.will_retry),
            turnActive: true,
            status: { kind: "running" },
          };
      break;
    case "tool.requested": {
      const name = event.payload.name || "?";
      next = {
        ...next,
        messages: applyToolRequestedToMessages(next.messages, {
          turnID: event.turn_id,
          toolUseID: event.payload.tool_use_id,
          toolName: name,
          input: event.payload.input ?? undefined,
          timeoutSeconds: event.payload.timeout_seconds,
        }),
        turnActive: true,
        status: { kind: "tool", name },
      };
      break;
    }
    case "tool.completed":
      next = {
        ...appendToolResult(next, event, false),
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    case "tool.output_delta":
      next = {
        ...next,
        messages: applyToolOutputDeltaToMessages(next.messages, {
          turnID: event.turn_id,
          toolUseID: event.payload.tool_use_id,
          toolName: event.payload.name || "exec_command",
          text: event.payload.text,
        }),
        turnActive: true,
        status: { kind: "tool", name: event.payload.name || "exec_command" },
      };
      break;
    case "tool.errored":
      next = {
        ...appendToolResult(next, event, true),
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    case "hook.started":
      next = { ...next, turnActive: true, status: { kind: "running" } };
      break;
    case "hook.completed":
    case "hook.errored":
      next = { ...next, turnActive: true, status: { kind: "running" } };
      break;
    case "hook.trace":
      next = {
        ...next,
        messages: appendHookTraceMessage(next.messages, event),
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    case "pending_input.queued":
      next = projectQueuedInput(
        next,
        event.payload.input,
        event.payload.kind,
        event.payload.pending_count,
      );
      break;
    case "pending_input.draining":
      next = reserveDrainingQueuedInputs(next, event.payload.count);
      next = {
        ...next,
        turnActive: true,
        status: { kind: "pending", count: event.payload.pending_count },
      };
      break;
    case "pending_input.promoted": {
      const promoted = drainQueuedInputState(next.queuedInput, 1);
      const item = promoted.drained[0];
      next = {
        ...next,
        queuedInput: promoted.state,
        messages: item
          ? appendLiveTurnToMessages(
              next.messages,
              event.turn_id,
              item.input,
              item.kind,
              "event",
              item.attachments,
            )
          : next.messages,
        turnActive: true,
        status: { kind: "running" },
      };
      break;
    }
    case "pending_input.drained":
      next = drainQueuedInputs(next, event.payload.count, event.turn_id);
      next = { ...next, turnActive: true, status: { kind: "running" } };
      break;
    case "pending_input.dropped":
      next = dropQueuedInputs(next, event.payload.count);
      next = markProjectionError(
        next,
        `${event.payload.count} pending input(s) dropped`,
      );
      break;
    case "pending_input.rejected":
      next = {
        ...markProjectionError(
          next,
          event.payload.reason || "pending input queue full",
        ),
        turnActive: true,
      };
      break;
    case "turn.completed":
      next = {
        ...markProjectionDone(clearQueuedInputs(next)),
        turnActive: false,
      };
      effects.push({ type: "refresh" }, { type: "scheduleIdleStatus" });
      break;
    case "turn.errored":
      next = {
        ...markProjectionError(clearQueuedInputs(next), event.payload.error),
        turnActive: false,
      };
      effects.push({ type: "refresh" });
      break;
    case "context.compact.started":
      next = projectPendingCompact(next);
      break;
    case "context.compact.completed":
      next = {
        ...clearLocalCompactMessages(next),
        contextUsage: event.payload.context_usage ?? next.contextUsage,
        compactActive: false,
      };
      effects.push({ type: "refresh", preserveLiveMessages: true });
      if (!next.turnActive && next.queuedInput.items.length === 0) {
        next = markProjectionDone(next);
        effects.push({ type: "scheduleIdleStatus" });
      }
      break;
    case "context.compact.errored":
      next = {
        ...markProjectionError(
          clearLocalCompactMessages(next),
          event.payload.error,
        ),
        compactActive: false,
      };
      effects.push({ type: "refresh", preserveLiveMessages: true });
      break;
    case "context.compact.summary_retry":
    case "context.compact.summary_model_fallback":
    case "context.compact.skipped":
    case "context.projection.applied":
      break;
  }

  return { state: next, effects };
}

export function projectTurnStatusReconcile(
  state: LiveSessionProjection,
  turn: TurnStatusResponse,
): LiveSessionProjectionResult {
  if (turn.state === "running") {
    return {
      state: {
        ...state,
        turnActive: true,
        status:
          turn.pending_count && turn.pending_count > 0
            ? { kind: "pending", count: turn.pending_count }
            : { kind: "running" },
      },
      effects: [],
    };
  }
  if (turn.state === "errored") {
    return {
      state: {
        ...markProjectionError(clearQueuedInputs(state), turn.error),
        turnActive: false,
      },
      effects: [{ type: "refresh" }],
    };
  }
  return {
    state: {
      ...markProjectionDone(clearQueuedInputs(state)),
      turnActive: false,
    },
    effects: [{ type: "refresh" }, { type: "scheduleIdleStatus" }],
  };
}

export function projectSessionTurnStatus(
  state: LiveSessionProjection,
  turn: SessionTurnStatus | undefined,
): LiveSessionProjection {
  if (!turn?.turn_id) return state;
  if (turn.state !== "running") {
    return projectTurnStatusReconcile(state, turn).state;
  }
  const result = projectTurnStatusReconcile(
    {
      ...state,
      messages: ensurePendingAssistant(state.messages, turn.turn_id),
    },
    turn,
  );
  return result.state;
}

function consumeQueuedInput(
  state: LiveSessionProjection,
  input: string | undefined,
  kind: string | undefined,
): { state: LiveSessionProjection; item?: QueuedInput } {
  const normalizedInput = input ?? "";
  let index = state.queuedInput.items.findIndex(
    (item) => item.input === normalizedInput && item.kind === kind,
  );
  if (index < 0 && normalizedInput === "" && state.queuedInput.items.length > 0) {
    index = 0;
  }
  if (index < 0) return { state };
  const item = state.queuedInput.items[index];
  return {
    state: {
      ...state,
      queuedInput: {
        ...state.queuedInput,
        items: [
          ...state.queuedInput.items.slice(0, index),
          ...state.queuedInput.items.slice(index + 1),
        ],
      },
    },
    item,
  };
}

function drainQueuedInputs(
  state: LiveSessionProjection,
  count: number,
  turnID: string | undefined,
): LiveSessionProjection {
  const reservedCount = Math.min(count, state.drainingQueuedInputs.length);
  const reserved = state.drainingQueuedInputs
    .slice(0, reservedCount)
    .filter((item): item is QueuedInput => item !== undefined);
  const result = drainQueuedInputState(state.queuedInput, count - reservedCount);
  return {
    ...state,
    queuedInput: result.state,
    drainingQueuedInputs: state.drainingQueuedInputs.slice(reservedCount),
    messages: appendDrainedInputs(
      state.messages,
      [...reserved, ...result.drained],
      turnID,
    ),
  };
}

function reserveDrainingQueuedInputs(
  state: LiveSessionProjection,
  count: number,
): LiveSessionProjection {
  if (count <= 0) return state;
  const result = drainQueuedInputState(state.queuedInput, count);
  const reserved: Array<QueuedInput | undefined> = [];
  for (let index = 0; index < count; index++) {
    reserved.push(result.drained[index]);
  }
  return {
    ...state,
    queuedInput: result.state,
    drainingQueuedInputs: [...state.drainingQueuedInputs, ...reserved],
  };
}

function dropQueuedInputs(
  state: LiveSessionProjection,
  count: number,
): LiveSessionProjection {
  return {
    ...state,
    queuedInput: dropQueuedInputState(state.queuedInput, count),
  };
}

function appendDrainedInputs(
  messages: Message[],
  items: QueuedInput[],
  turnID: string | undefined,
): Message[] {
  if (!items.length) return messages;
  const additions: Message[] = items.map((item) => ({
    role: "user",
    turn_id: turnID,
    kind: item.kind || "pending_input",
    blocks: inputBlocks(item.input, item.attachments),
  }));
  if (!turnID) return [...messages, ...additions];
  const insertAt = messages.findIndex(
    (message) =>
      message.turn_id === turnID &&
      message.role === "assistant" &&
      message.pending,
  );
  if (insertAt < 0) return [...messages, ...additions];
  return [
    ...messages.slice(0, insertAt),
    ...additions,
    ...messages.slice(insertAt),
  ];
}

function appendLiveTurnToMessages(
  messages: Message[],
  turnID: string | undefined,
  input: string | undefined,
  kind: string | undefined,
  source: "event" | "optimistic",
  attachments: MediaRef[] = [],
): Message[] {
  const blocks = inputBlocks(input, attachments);
  if (!turnID || blocks.length === 0) return messages;
  if (messages.some((message) => message.turn_id === turnID)) return messages;
  const existingTurnID = input
    ? findPendingTurnForInput(messages, input)
    : undefined;
  if (existingTurnID) {
    if (source === "optimistic") return messages;
    return messages.map((message) =>
      message.turn_id === existingTurnID ? { ...message, turn_id: turnID } : message,
    );
  }
  return [
    ...messages,
    {
      role: "user",
      turn_id: turnID,
      kind,
      blocks,
    },
    {
      role: "assistant",
      turn_id: turnID,
      pending: true,
      blocks: [],
    },
  ];
}

function inputBlocks(
  input: string | undefined,
  attachments: MediaRef[] | undefined,
): Block[] {
  const blocks: Block[] = [];
  if (input) {
    blocks.push({ type: "text", text: input });
  }
  for (const media of attachments ?? []) {
    blocks.push({ type: "image", media });
  }
  return blocks;
}

function ensurePendingAssistant(messages: Message[], turnID: string): Message[] {
  if (!turnID) return messages;
  if (
    messages.some(
      (message) =>
        message.turn_id === turnID && message.role === "assistant",
    )
  ) {
    return messages;
  }
  return [
    ...messages,
    {
      role: "assistant",
      turn_id: turnID,
      pending: true,
      blocks: [],
    },
  ];
}

function findPendingTurnForInput(
  messages: Message[],
  input: string,
): string | undefined {
  for (const message of messages) {
    if (message.role !== "user" || messageText(message) !== input) continue;
    const turnID = message.turn_id;
    if (
      turnID &&
      messages.some(
        (candidate) =>
          candidate.role === "assistant" &&
          candidate.turn_id === turnID &&
          candidate.pending,
      )
    ) {
      return turnID;
    }
  }
  return undefined;
}

function messageText(message: Message): string | undefined {
  const block = message.blocks?.find((candidate) => candidate.type === "text");
  return block && "text" in block && typeof block.text === "string"
    ? block.text
    : undefined;
}

function applyAssistantResponse(
  state: LiveSessionProjection,
  event: Extract<BrowserEvent, { type: "llm.responded" }>,
): LiveSessionProjection {
  if (!event.turn_id) return state;
  const blocks = assistantBlocksFromEventPayload(event.payload);
  const model = event.payload.model;
  const pendingIndex = state.messages.findIndex(
    (message) =>
      message.turn_id === event.turn_id &&
      message.role === "assistant" &&
      message.pending,
  );
  if (pendingIndex >= 0) {
    const messages = [...state.messages];
    messages[pendingIndex] = {
      ...messages[pendingIndex],
      pending: false,
      blocks,
      model,
    };
    if (event.payload.notice) {
      messages.splice(pendingIndex, 0, {
        ...event.payload.notice,
        turn_id: event.turn_id,
      });
    }
    return { ...state, messages };
  }
  const appended = event.payload.notice
    ? [{ ...event.payload.notice, turn_id: event.turn_id }]
    : [];
  return {
    ...state,
    messages: [
      ...state.messages,
      ...appended,
      { role: "assistant", turn_id: event.turn_id, pending: false, blocks, model },
    ],
  };
}

function resetPendingAssistantOutput(
  state: LiveSessionProjection,
  turnID: string | undefined,
  willRetry: boolean,
): LiveSessionProjection {
  if (!turnID || !willRetry) return state;
  return {
    ...state,
    messages: state.messages.map((message) =>
      message.turn_id === turnID && message.role === "assistant" && message.pending
        ? { ...message, blocks: [] }
        : message,
    ),
  };
}

function applyAssistantOutputDelta(
  state: LiveSessionProjection,
  event: Extract<BrowserEvent, { type: "llm.output_delta" }>,
): LiveSessionProjection {
  if (!event.turn_id || !event.payload.text) return state;
  const blockType =
    event.payload.kind === "reasoning"
      ? "reasoning"
      : event.payload.kind === "text"
        ? "text"
        : null;
  if (!blockType) return state;

  const messageIndex = state.messages.findLastIndex(
    (message) =>
      message.turn_id === event.turn_id &&
      message.role === "assistant" &&
      message.pending,
  );
  const messages = [...state.messages];
  const message: Message =
    messageIndex >= 0
      ? { ...messages[messageIndex], blocks: [...(messages[messageIndex].blocks ?? [])] }
      : {
          role: "assistant",
          turn_id: event.turn_id,
          pending: true,
          blocks: [],
        };
  const blocks = message.blocks ?? [];
  const blockIndex = blocks.findIndex(
    (block) =>
      block.type === blockType && block.stream_index === event.payload.index,
  );
  if (blockIndex >= 0) {
    const block = blocks[blockIndex];
    if (block.type === "text" || block.type === "reasoning") {
      blocks[blockIndex] = {
        ...block,
        text: `${block.text ?? ""}${event.payload.text}`,
      };
    }
  } else if (blockType === "reasoning") {
    blocks.push({
      type: "reasoning",
      text: event.payload.text,
      stream_index: event.payload.index,
    });
  } else {
    blocks.push({
      type: "text",
      text: event.payload.text,
      stream_index: event.payload.index,
    });
  }
  message.blocks = blocks;
  if (event.payload.model) message.model = event.payload.model;
  if (messageIndex >= 0) {
    messages[messageIndex] = message;
  } else {
    messages.push(message);
  }
  return { ...state, messages };
}

function appendToolResult(
  state: LiveSessionProjection,
  event: Extract<BrowserEvent, { type: "tool.completed" | "tool.errored" }>,
  isError: boolean,
): LiveSessionProjection {
  if (!event.turn_id) return state;
  const content =
    event.type === "tool.errored"
      ? event.payload.error || event.payload.preview || ""
      : event.payload.preview || "";
  return {
    ...state,
    messages: applyToolResultToMessages(state.messages, {
      turnID: event.turn_id,
      toolUseID: event.payload.tool_use_id,
      toolName: event.payload.name || "exec_command",
      content,
      media: event.payload.media,
      isError,
      timeoutSeconds: event.payload.timeout_seconds,
    }),
  };
}

function appendHookTraceMessage(
  messages: Message[],
  event: Extract<BrowserEvent, { type: "hook.trace" }>,
): Message[] {
  const text = event.payload.text;
  if (!text) return messages;
  const id = event.id ? `hook-${event.id}` : undefined;
  if (id && messages.some((message) => message.id === id)) return messages;
  return [
    ...messages,
    {
      id,
      role: "system",
      kind: "hook_event",
      turn_id: event.turn_id,
      blocks: [{ type: "text", text }],
    },
  ];
}

function tokenUsageFromEvent(
  event: Extract<BrowserEvent, { type: "llm.responded" }>,
): TokenUsage | null {
  const usage = event.payload.token_usage ?? event.payload.usage;
  if (!usage) return null;
  return usage;
}
