import {
  clearLiveThreadTranscript,
  clearLocalCompactMessages,
  createLiveThreadProjection,
  projectCommandResult,
  projectCompactCommand,
  projectLiveThreadEvent,
  projectOptimisticTurn,
  projectPendingCompact,
  projectQueuedInput,
  resetLiveThreadProjection,
  type LiveThreadProjection,
  type LiveThreadProjectionEffect,
} from "./live-thread-projection.ts";
import { isCompactCommandInput } from "./compact-ui.ts";
import { mergeOlderThreadPage } from "./thread-messages.ts";
import type {
  ActiveContextSnapshot,
  BrowserEvent,
  MediaRef,
  ThreadShowResponse,
  StartTurnResponse,
} from "../types.ts";

export type ThreadReadState = {
  data: ThreadShowResponse | null;
  loadError: string | null;
  projection: LiveThreadProjection;
  activeContext: ActiveContextSnapshot | null;
  composerHint: string | null;
  submitError: string | null;
  loadingOlderMessages: boolean;
  olderMessagesError: string | null;
};

export type ThreadReadEffect =
  | LiveThreadProjectionEffect
  | { type: "scheduleComposerHintClear" };

export type ThreadReadResult = {
  state: ThreadReadState;
  effects: ThreadReadEffect[];
};

export type ThreadLiveSubscription = {
  threadID: string;
  cursor: string;
};

// captureThreadLiveSubscription holds the cursor the live SSE subscription
// resumes from. It returns the identical object while that cursor is unchanged,
// because the subscription effect keys off this value and would otherwise
// rebuild the stream on every transcript refresh. A thread whose journal is
// still empty reports an empty cursor; that placeholder must be replaced once a
// refresh carries a real one, or every later reconnect would claim the browser
// has seen nothing and pull a full journal replay.
export function captureThreadLiveSubscription(
  current: ThreadLiveSubscription | null,
  data: ThreadShowResponse,
): ThreadLiveSubscription {
  if (current?.threadID !== data.id) {
    return { threadID: data.id, cursor: data.event_cursor };
  }
  if (current.cursor !== "" || data.event_cursor === "") return current;
  return { threadID: data.id, cursor: data.event_cursor };
}

export function createThreadReadState(): ThreadReadState {
  return {
    data: null,
    loadError: null,
    projection: createLiveThreadProjection(),
    activeContext: null,
    composerHint: null,
    submitError: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function resetThreadReadState(
  state: ThreadReadState,
): ThreadReadState {
  return {
    ...state,
    data: null,
    loadError: null,
    projection: resetLiveThreadProjection(),
    activeContext: null,
    composerHint: null,
    submitError: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function projectThreadLoaded(
  state: ThreadReadState,
  data: ThreadShowResponse,
  opts?: { preserveLiveMessages?: boolean },
): ThreadReadState {
  const projection = opts?.preserveLiveMessages
    ? reconcilePersistedLiveMessages(state.projection, data.messages)
    : clearLiveThreadTranscript(state.projection);
  return {
    ...state,
    data,
    loadError: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
    projection,
  };
}

function reconcilePersistedLiveMessages(
  projection: LiveThreadProjection,
  persistedMessages: ThreadShowResponse["messages"],
): LiveThreadProjection {
  const persistedIDs = new Set(
    persistedMessages
      .map((message) => message.id)
      .filter((id): id is string => typeof id === "string" && id.length > 0),
  );
  if (persistedIDs.size === 0) return projection;

  const messages = projection.messages.filter(
    (message) => !message.id || !persistedIDs.has(message.id),
  );
  if (messages.length === projection.messages.length) return projection;
  return { ...projection, messages };
}

export function projectThreadLoadFailed(
  state: ThreadReadState,
  error: unknown,
): ThreadReadState {
  return {
    ...state,
    data: null,
    loadError: errorMessage(error),
    activeContext: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function projectActiveContextLoaded(
  state: ThreadReadState,
  activeContext: ActiveContextSnapshot,
): ThreadReadState {
  return { ...state, activeContext };
}

export function projectActiveContextFailed(
  state: ThreadReadState,
): ThreadReadState {
  return { ...state, activeContext: null };
}

export function projectLoadOlderStarted(
  state: ThreadReadState,
): ThreadReadState {
  return { ...state, loadingOlderMessages: true, olderMessagesError: null };
}

export function projectLoadOlderSucceeded(
  state: ThreadReadState,
  page: ThreadShowResponse,
): ThreadReadState {
  return {
    ...state,
    data: mergeOlderThreadPage(state.data, page),
    loadingOlderMessages: false,
  };
}

export function projectLoadOlderFailed(
  state: ThreadReadState,
  error: unknown,
): ThreadReadState {
  return {
    ...state,
    loadingOlderMessages: false,
    olderMessagesError: errorMessage(error, "Failed to load older messages."),
  };
}

export function projectLiveBrowserEvent(
  state: ThreadReadState,
  event: BrowserEvent,
): ThreadReadResult {
  const metadataState = projectThreadMetadataEvent(state, event);
  if (
    eventTranscriptAlreadyLoaded(
      state.data?.messages ?? [],
      state.projection,
      event,
    )
  ) {
    return { state: metadataState, effects: [] };
  }
  const result = projectLiveThreadEvent(state.projection, event);
  return withProjectionResult(
    metadataState,
    result.state,
    result.effects,
  );
}

export function projectPromptInputChanged(
  state: ThreadReadState,
): ThreadReadState {
  let next = state.submitError ? { ...state, submitError: null } : state;
  if (next.composerHint) {
    next = { ...next, composerHint: null };
  }
  return next;
}

export function projectComposerHint(
  state: ThreadReadState,
  message: string,
): ThreadReadResult {
  return {
    state: { ...state, composerHint: message },
    effects: [{ type: "scheduleComposerHintClear" }],
  };
}

export function clearComposerHint(state: ThreadReadState): ThreadReadState {
  return { ...state, composerHint: null };
}

export function projectPendingSubmit(
  state: ThreadReadState,
  prompt: string,
  submittedAt?: string,
): ThreadReadState {
  state = state.submitError ? { ...state, submitError: null } : state;
  if (!isCompactCommandInput(prompt)) return state;
  return {
    ...state,
    projection: projectPendingCompact(state.projection, prompt, submittedAt),
  };
}

export function projectStartTurnSucceeded(
  state: ThreadReadState,
  prompt: string,
  turn: StartTurnResponse,
  attachments: MediaRef[] = [],
  submittedAt?: string,
): ThreadReadResult {
  state = state.submitError ? { ...state, submitError: null } : state;
  if (turn.command) {
    return projectCommandTurnSucceeded(state, prompt, turn, submittedAt);
  }
  if (turn.queued) {
    return withStartTurnWarnings({
      state: {
        ...state,
        projection: projectQueuedInput(
          state.projection,
          prompt,
          undefined,
          turn.pending_count ?? 0,
          attachments,
          undefined,
          submittedAt,
        ),
      },
      effects: [],
    }, turn);
  }
  if (!turn.turn_id) {
    return projectStartTurnFailed(
      state,
      isCompactCommandInput(prompt),
      new Error("turn response missing turn_id"),
    );
  }
  return withStartTurnWarnings({
    state: {
      ...state,
      projection: projectOptimisticTurn(
        state.projection,
        turn.turn_id,
        prompt,
        undefined,
        attachments,
        submittedAt,
      ),
    },
    effects: [],
  }, turn);
}

function withStartTurnWarnings(
  result: ThreadReadResult,
  turn: StartTurnResponse,
): ThreadReadResult {
  const warnings = (turn.warnings ?? [])
    .map((warning) =>
      [warning.message, warning.suggestion].filter(Boolean).join("; "),
    )
    .filter(Boolean);
  if (warnings.length === 0) return result;
  return {
    state: {
      ...result.state,
      composerHint: `Warning: ${warnings.join("; ")}`,
    },
    effects: [...result.effects, { type: "scheduleComposerHintClear" }],
  };
}

export function projectStartTurnFailed(
  state: ThreadReadState,
  compactCommand: boolean,
  error: unknown,
): ThreadReadResult {
  const detail = errorMessage(error, "Failed to start turn.");
  let projection = state.projection;
  if (compactCommand) {
    projection = clearLocalCompactMessages(projection);
  }
  return {
    state: {
      ...state,
      submitError: detail,
      projection,
    },
    effects: [],
  };
}

function projectCommandTurnSucceeded(
  state: ThreadReadState,
  prompt: string,
  turn: StartTurnResponse,
  submittedAt?: string,
): ThreadReadResult {
  const command = turn.command;
  if (!command) {
    return { state, effects: [] };
  }
  if (command.name === "/new") {
    return {
      state,
      effects: [{ type: "refresh", preserveLiveMessages: true }],
    };
  }
  if (command.name === "/compact") {
    let projection: LiveThreadProjection = clearLocalCompactMessages(
      state.projection,
    );
    const effects: ThreadReadEffect[] = [
      { type: "refresh", preserveLiveMessages: true },
    ];
    if (command.compact?.message_id) {
      projection = projectCompactCommand(
        projection,
        command.compact.message_id,
        prompt,
        submittedAt,
      );
    } else {
      projection = projectCommandResult(
        projection,
        prompt,
        command.text ?? "",
        submittedAt,
      );
    }
    return { state: { ...state, projection }, effects };
  }
  return {
    state: {
      ...state,
      projection: projectCommandResult(
        state.projection,
        prompt,
        command.text ?? "",
        submittedAt,
      ),
    },
    effects: [],
  };
}

function projectThreadMetadataEvent(
  state: ThreadReadState,
  event: BrowserEvent,
): ThreadReadState {
  if (!state.data) return state;
  if (event.type === "goal.updated") {
    return {
      ...state,
      data: {
        ...state.data,
        goal: event.payload,
      },
    };
  }
  if (event.type !== "notes.updated") return state;
  return {
    ...state,
    data: {
      ...state.data,
      notes: event.payload,
    },
  };
}

function eventTranscriptAlreadyLoaded(
  persistedMessages: ThreadShowResponse["messages"],
  projection: LiveThreadProjection,
  event: BrowserEvent,
): boolean {
  const messages = [...persistedMessages, ...projection.messages];
  switch (event.type) {
    case "turn.started":
    case "llm.responded":
    case "policy.trace":
      return Boolean(
        event.payload.message_id &&
          messages.some((message) => message.id === event.payload.message_id),
      );
    case "pending_input.queued":
      return Boolean(
        event.payload.message_id &&
          (messages.some(
            (message) => message.id === event.payload.message_id,
          ) ||
            projection.queuedInput.items.some(
              (item) => item.messageID === event.payload.message_id,
            ) ||
            projection.drainingQueuedInputs.some(
              (item) => item?.messageID === event.payload.message_id,
            )),
      );
    case "tool.requested":
      return Boolean(
        event.payload.tool_use_id &&
          messages.some((message) =>
            message.blocks?.some(
              (block) =>
                block.type === "tool_use" &&
                block.tool_use_id === event.payload.tool_use_id,
            ),
          ),
      );
    case "tool.completed":
    case "tool.errored":
    case "tool.outcome_unknown":
      return Boolean(
        event.payload.tool_use_id &&
          messages.some((message) =>
            message.blocks?.some(
              (block) =>
                block.type === "tool_result" &&
                block.tool_use_id === event.payload.tool_use_id,
            ),
          ),
      );
    case "transcript.repaired":
      return Boolean(
        event.payload.repairs.length > 0 &&
          event.payload.repairs.every(
            (repair) =>
              repair.repair_message_id &&
              messages.some(
                (message) => message.id === repair.repair_message_id,
              ),
          ),
      );
    default:
      return false;
  }
}

function errorMessage(
  error: unknown,
  fallback = "Failed to load conversation.",
): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  if (error && typeof error === "object") {
    const record = error as Record<string, unknown>;
    if (typeof record.message === "string" && record.message.trim()) {
      return record.message;
    }
    if (typeof record.error === "string" && record.error.trim()) {
      return record.error;
    }
  }
  if (typeof error === "string" && error.trim()) {
    return error;
  }
  return fallback;
}

function withProjectionResult(
  state: ThreadReadState,
  projection: LiveThreadProjection,
  effects: LiveThreadProjectionEffect[],
): ThreadReadResult {
  return {
    state: { ...state, projection },
    effects,
  };
}
