import {
  clearLiveSessionTranscript,
  clearLocalCompactMessages,
  createLiveSessionProjection,
  projectCommandResult,
  projectCompactCommand,
  projectLiveSessionEvent,
  projectOptimisticTurn,
  projectPendingCompact,
  projectQueuedInput,
  resetLiveSessionProjection,
  type LiveSessionProjection,
  type LiveSessionProjectionEffect,
} from "./live-session-projection.ts";
import { isCompactCommandInput } from "./compact-ui.ts";
import { mergeOlderSessionPage } from "./session-messages.ts";
import type {
  ActiveContextSnapshot,
  BrowserEvent,
  MediaRef,
  SessionShowResponse,
  SlashCommandResponse,
  StartTurnResponse,
} from "../types.ts";

export type SessionInitialCommandState = {
  commandInput?: string;
  command?: SlashCommandResponse;
} | null;

export type SessionReadState = {
  data: SessionShowResponse | null;
  loadError: string | null;
  projection: LiveSessionProjection;
  activeContext: ActiveContextSnapshot | null;
  composerHint: string | null;
  submitError: string | null;
  loadingOlderMessages: boolean;
  olderMessagesError: string | null;
};

export type SessionReadEffect =
  | LiveSessionProjectionEffect
  | { type: "scheduleComposerHintClear" }
  | { type: "clearRouteState" }
  | { type: "dispatchSessionsChanged" }
  | {
      type: "navigateToSession";
      sessionID: string;
      state: SessionInitialCommandState;
    };

export type SessionReadResult = {
  state: SessionReadState;
  effects: SessionReadEffect[];
};

export function createSessionReadState(): SessionReadState {
  return {
    data: null,
    loadError: null,
    projection: createLiveSessionProjection(),
    activeContext: null,
    composerHint: null,
    submitError: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function resetSessionReadState(
  state: SessionReadState,
): SessionReadState {
  return {
    ...state,
    data: null,
    loadError: null,
    projection: resetLiveSessionProjection(),
    activeContext: null,
    composerHint: null,
    submitError: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function projectSessionLoaded(
  state: SessionReadState,
  data: SessionShowResponse,
  opts?: { preserveLiveMessages?: boolean },
): SessionReadState {
  const projection = opts?.preserveLiveMessages
    ? state.projection
    : clearLiveSessionTranscript(state.projection);
  return {
    ...state,
    data,
    loadError: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
    projection,
  };
}

export function projectSessionLoadFailed(
  state: SessionReadState,
  error: unknown,
): SessionReadState {
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
  state: SessionReadState,
  activeContext: ActiveContextSnapshot,
): SessionReadState {
  return { ...state, activeContext };
}

export function projectActiveContextFailed(
  state: SessionReadState,
): SessionReadState {
  return { ...state, activeContext: null };
}

export function projectLoadOlderStarted(
  state: SessionReadState,
): SessionReadState {
  return { ...state, loadingOlderMessages: true, olderMessagesError: null };
}

export function projectLoadOlderSucceeded(
  state: SessionReadState,
  page: SessionShowResponse,
): SessionReadState {
  return {
    ...state,
    data: mergeOlderSessionPage(state.data, page),
    loadingOlderMessages: false,
  };
}

export function projectLoadOlderFailed(
  state: SessionReadState,
  error: unknown,
): SessionReadState {
  return {
    ...state,
    loadingOlderMessages: false,
    olderMessagesError: errorMessage(error, "Failed to load older messages."),
  };
}

export function projectLiveBrowserEvent(
  state: SessionReadState,
  event: BrowserEvent,
): SessionReadResult {
  const result = projectLiveSessionEvent(state.projection, event);
  return withProjectionResult(
    projectSessionMetadataEvent(state, event),
    result.state,
    result.effects,
  );
}

export function projectInitialCommand(
  state: SessionReadState,
  commandInput: string,
  command: SlashCommandResponse,
): SessionReadResult {
  let next = state.projection;
  const effects: SessionReadEffect[] = [{ type: "clearRouteState" }];
  if (command.name === "/compact" && command.compact?.message_id) {
    next = projectCompactCommand(next, command.compact.message_id, commandInput);
    effects.unshift({ type: "refresh", preserveLiveMessages: true });
  } else {
    next = projectCommandResult(next, commandInput, command.text ?? "");
  }
  return { state: { ...state, projection: next }, effects };
}

export function projectPromptInputChanged(
  state: SessionReadState,
): SessionReadState {
  let next = state.submitError ? { ...state, submitError: null } : state;
  if (next.composerHint) {
    next = { ...next, composerHint: null };
  }
  return next;
}

export function projectComposerHint(
  state: SessionReadState,
  message: string,
): SessionReadResult {
  return {
    state: { ...state, composerHint: message },
    effects: [{ type: "scheduleComposerHintClear" }],
  };
}

export function clearComposerHint(state: SessionReadState): SessionReadState {
  return { ...state, composerHint: null };
}

export function projectPendingSubmit(
  state: SessionReadState,
  prompt: string,
): SessionReadState {
  state = state.submitError ? { ...state, submitError: null } : state;
  if (!isCompactCommandInput(prompt)) return state;
  return { ...state, projection: projectPendingCompact(state.projection, prompt) };
}

export function projectStartTurnSucceeded(
  state: SessionReadState,
  prompt: string,
  turn: StartTurnResponse,
  attachments: MediaRef[] = [],
): SessionReadResult {
  state = state.submitError ? { ...state, submitError: null } : state;
  if (turn.command) {
    return projectCommandTurnSucceeded(state, prompt, turn);
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
      ),
    },
    effects: [],
  }, turn);
}

function withStartTurnWarnings(
  result: SessionReadResult,
  turn: StartTurnResponse,
): SessionReadResult {
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
  state: SessionReadState,
  compactCommand: boolean,
  error: unknown,
): SessionReadResult {
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
  state: SessionReadState,
  prompt: string,
  turn: StartTurnResponse,
): SessionReadResult {
  const command = turn.command;
  if (!command) {
    return { state, effects: [] };
  }
  if (command.name === "/new" && command.status?.session_id) {
    return {
      state,
      effects: [
        { type: "dispatchSessionsChanged" },
        {
          type: "navigateToSession",
          sessionID: command.status.session_id,
          state: turn.turn_id ? null : { commandInput: prompt, command },
        },
      ],
    };
  }
  if (command.name === "/compact") {
    let projection: LiveSessionProjection = clearLocalCompactMessages(
      state.projection,
    );
    const effects: SessionReadEffect[] = [
      { type: "refresh", preserveLiveMessages: true },
    ];
    if (command.compact?.message_id) {
      projection = projectCompactCommand(
        projection,
        command.compact.message_id,
        prompt,
      );
    } else {
      projection = projectCommandResult(projection, prompt, command.text ?? "");
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
      ),
    },
    effects: [],
  };
}

function projectSessionMetadataEvent(
  state: SessionReadState,
  event: BrowserEvent,
): SessionReadState {
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
  state: SessionReadState,
  projection: LiveSessionProjection,
  effects: LiveSessionProjectionEffect[],
): SessionReadResult {
  return {
    state: { ...state, projection },
    effects,
  };
}
