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
  MediaRef,
  Message,
} from "../types.ts";

export type LiveSessionProjection = {
  messages: Message[];
  queuedInput: QueuedInputState;
  // Empty slots reserve announced drain positions missing from local queue state.
  drainingQueuedInputs: Array<QueuedInput | undefined>;
  compactAdmissionTurnID: string | null;
  compactCommandInputs: Record<string, string>;
};

export type LiveSessionProjectionEffect = {
  type: "refresh";
  preserveLiveMessages?: boolean;
};

export type LiveSessionProjectionResult = {
  state: LiveSessionProjection;
  effects: LiveSessionProjectionEffect[];
};

export function createLiveSessionProjection(): LiveSessionProjection {
  return {
    messages: [],
    queuedInput: createQueuedInputState(),
    drainingQueuedInputs: [],
    compactAdmissionTurnID: null,
    compactCommandInputs: {},
  };
}

export function resetLiveSessionProjection(): LiveSessionProjection {
  return createLiveSessionProjection();
}

export function clearLiveSessionTranscript(
  state: LiveSessionProjection,
): LiveSessionProjection {
  return { ...state, messages: [] };
}

export function clearQueuedInputs(
  state: LiveSessionProjection,
): LiveSessionProjection {
  if (
    state.queuedInput.items.length === 0 &&
    state.drainingQueuedInputs.length === 0 &&
    state.compactAdmissionTurnID === null
  ) {
    return state;
  }
  return {
    ...state,
    queuedInput: createQueuedInputState(),
    drainingQueuedInputs: [],
    compactAdmissionTurnID: null,
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
  };
}

export function projectQueuedInput(
  state: LiveSessionProjection,
  input: string | undefined,
  kind: string | undefined,
  pendingCount: number,
  attachments: MediaRef[] = [],
  messageID?: string,
): LiveSessionProjection {
  return {
    ...state,
    queuedInput: enqueueQueuedInputState(
      state.queuedInput,
      input,
      kind,
      pendingCount,
      attachments,
      messageID,
    ),
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
  };
}

export function projectLiveSessionEvent(
  state: LiveSessionProjection,
  event: BrowserEvent,
): LiveSessionProjectionResult {
  let next = state;
  const effects: LiveSessionProjectionEffect[] = [];

  switch (event.type) {
    case "turn.admitted":
      if (event.turn_id && event.payload.non_interruptible) {
        next = { ...next, compactAdmissionTurnID: event.turn_id };
      }
      break;
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
          event.payload.message_id ?? consumed.item?.messageID,
        ),
      };
      break;
    }
    case "llm.requested":
      break;
    case "llm.output_delta":
      next = applyAssistantOutputDelta(next, event);
      break;
    case "llm.responded":
      next = applyAssistantResponse(next, event);
      break;
    case "llm.retry":
      if (event.payload.purpose !== "compaction") {
        next = resetPendingAssistantOutput(
          next,
          event.turn_id,
          event.payload.will_retry,
        );
      }
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
      };
      break;
    }
    case "tool.completed":
      next = appendToolResult(next, event, false);
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
      };
      break;
    case "tool.errored":
      next = appendToolResult(next, event, true);
      break;
    case "hook.started":
    case "hook.completed":
    case "hook.errored":
      break;
    case "hook.trace":
      next = {
        ...next,
        messages: appendHookTraceMessage(next.messages, event),
      };
      break;
    case "pending_input.queued":
      next = projectQueuedInput(
        next,
        event.payload.input,
        event.payload.kind,
        event.payload.pending_count,
        [],
        event.payload.message_id,
      );
      break;
    case "pending_input.draining":
      next = reserveDrainingQueuedInputs(next, event.payload.count);
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
              item.messageID,
            )
          : next.messages,
      };
      break;
    }
    case "pending_input.drained":
      next = drainQueuedInputs(next, event.payload.count, event.turn_id);
      break;
    case "pending_input.dropped":
      next = dropQueuedInputs(next, event.payload.count);
      break;
    case "pending_input.rejected":
      break;
    case "turn.completed":
      next = settleTerminalQueuedInputs(next, event.turn_id);
      effects.push({ type: "refresh" });
      break;
    case "turn.errored":
      next = settleTerminalQueuedInputs(next, event.turn_id);
      effects.push({ type: "refresh" });
      break;
    case "context.compact.started":
      next = projectPendingCompact(next);
      break;
    case "context.compact.completed":
      next = clearLocalCompactMessages(next);
      effects.push({ type: "refresh", preserveLiveMessages: true });
      break;
    case "context.compact.errored":
      next = clearLocalCompactMessages(next);
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

function settleTerminalQueuedInputs(
  state: LiveSessionProjection,
  turnID: string | undefined,
): LiveSessionProjection {
  if (turnID && state.compactAdmissionTurnID === turnID) {
    return { ...state, compactAdmissionTurnID: null };
  }
  return clearQueuedInputs(state);
}

function appendDrainedInputs(
  messages: Message[],
  items: QueuedInput[],
  turnID: string | undefined,
): Message[] {
  if (!items.length) return messages;
  const additions: Message[] = items.map((item) => ({
    id: item.messageID,
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
  messageID?: string,
): Message[] {
  const blocks = inputBlocks(input, attachments);
  if (!turnID || blocks.length === 0) return messages;
  if (messages.some((message) => message.turn_id === turnID)) {
    if (source !== "event" || !messageID) return messages;
    return messages.map((message) =>
      message.turn_id === turnID && message.role === "user" && !message.id
        ? { ...message, id: messageID }
        : message,
    );
  }
  const existingTurnID = input
    ? findPendingTurnForInput(messages, input)
    : undefined;
  if (existingTurnID) {
    if (source === "optimistic") return messages;
    return messages.map((message) =>
      message.turn_id === existingTurnID
        ? {
            ...message,
            turn_id: turnID,
            id: message.role === "user" ? messageID : message.id,
          }
        : message,
    );
  }
  return [
    ...messages,
    {
      id: messageID,
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
      id: event.payload.message_id ?? messages[pendingIndex].id,
      pending: false,
      blocks,
      model,
      created_at: event.ts,
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
      {
        id: event.payload.message_id,
        role: "assistant",
        turn_id: event.turn_id,
        pending: false,
        blocks,
        model,
        created_at: event.ts,
      },
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
  const id = event.payload.message_id ?? (event.id ? `hook-${event.id}` : undefined);
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
