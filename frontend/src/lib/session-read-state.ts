import {
  clearLiveSessionTranscript,
  clearLocalCompactMessages,
  createLiveSessionProjection,
  markProjectionDone,
  markProjectionError,
  markProjectionIdle,
  projectCommandResult,
  projectCompactCommand,
  projectLiveSessionEvent,
  projectOptimisticTurn,
  projectPendingCompact,
  projectQueuedInput,
  projectTurnStatusReconcile,
  resetLiveSessionProjection,
  type LiveSessionProjection,
  type LiveSessionProjectionEffect,
} from "./live-session-projection.ts";
import { isCompactCommandInput } from "./compact-ui.ts";
import { mergeOlderSessionPage } from "./session-messages.ts";
import type {
  ActiveContextSnapshot,
  BrowserEvent,
  SessionShowResponse,
  SlashCommandResponse,
  StartTurnResponse,
  TurnStatusResponse,
} from "../types.ts";

export type SessionInitialCommandState = {
  activeTurnID?: string;
  commandInput?: string;
  command?: SlashCommandResponse;
} | null;

export type SessionReadState = {
  data: SessionShowResponse | null;
  loadError: string | null;
  projection: LiveSessionProjection;
  activeContext: ActiveContextSnapshot | null;
  composerHint: string | null;
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
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function resetSessionReadState(
  state: SessionReadState,
  opts?: { activeTurnID?: string },
): SessionReadState {
  return {
    ...state,
    data: null,
    loadError: null,
    projection: resetLiveSessionProjection(opts),
    activeContext: null,
    composerHint: null,
    loadingOlderMessages: false,
    olderMessagesError: null,
  };
}

export function projectSessionLoaded(
  state: SessionReadState,
  data: SessionShowResponse,
  opts?: { preserveLiveMessages?: boolean },
): SessionReadState {
  return {
    ...state,
    data,
    loadError: null,
    olderMessagesError: null,
    projection: opts?.preserveLiveMessages
      ? state.projection
      : clearLiveSessionTranscript(state.projection),
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
    olderMessagesError: error instanceof Error ? error.message : String(error),
  };
}

export function projectLiveBrowserEvent(
  state: SessionReadState,
  event: BrowserEvent,
): SessionReadResult {
  const result = projectLiveSessionEvent(state.projection, event);
  return withProjectionResult(state, result.state, result.effects);
}

export function projectTurnStatus(
  state: SessionReadState,
  turn: TurnStatusResponse,
): SessionReadResult {
  const result = projectTurnStatusReconcile(state.projection, turn);
  return withProjectionResult(state, result.state, result.effects);
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
  return markProjectionDoneSoon({ ...state, projection: next }, effects);
}

export function projectPromptInputChanged(
  state: SessionReadState,
): SessionReadState {
  let next = state;
  if (next.composerHint) {
    next = { ...next, composerHint: null };
  }
  if (next.projection.status.kind === "error") {
    next = { ...next, projection: markProjectionIdle(next.projection) };
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
  if (!isCompactCommandInput(prompt)) return state;
  return { ...state, projection: projectPendingCompact(state.projection, prompt) };
}

export function projectStartTurnSucceeded(
  state: SessionReadState,
  prompt: string,
  turn: StartTurnResponse,
): SessionReadResult {
  if (turn.command) {
    return projectCommandTurnSucceeded(state, prompt, turn);
  }
  if (turn.queued) {
    return {
      state: {
        ...state,
        projection: projectQueuedInput(
          state.projection,
          prompt,
          undefined,
          turn.pending_count ?? 0,
        ),
      },
      effects: [],
    };
  }
  if (!turn.turn_id) {
    return projectStartTurnFailed(
      state,
      isCompactCommandInput(prompt),
      new Error("turn response missing turn_id"),
    );
  }
  return {
    state: {
      ...state,
      projection: projectOptimisticTurn(state.projection, turn.turn_id, prompt),
    },
    effects: [],
  };
}

export function projectStartTurnFailed(
  state: SessionReadState,
  compactCommand: boolean,
  error: unknown,
): SessionReadResult {
  let projection = state.projection;
  if (compactCommand) {
    projection = {
      ...clearLocalCompactMessages(projection),
      compactActive: false,
    };
  }
  return {
    state: {
      ...state,
      projection: markProjectionError(
        projection,
        error instanceof Error ? error.message : String(error),
      ),
    },
    effects: [],
  };
}

export function markSessionProjectionIdle(
  state: SessionReadState,
): SessionReadState {
  return { ...state, projection: markProjectionIdle(state.projection) };
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
          state: turn.turn_id
            ? { activeTurnID: turn.turn_id }
            : { commandInput: prompt, command },
        },
      ],
    };
  }
  if (command.name === "/compact") {
    let projection: LiveSessionProjection = {
      ...clearLocalCompactMessages(state.projection),
      compactActive: false,
    };
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
    const next = { ...state, projection };
    if (!projection.turnActive && projection.queuedInput.items.length === 0) {
      return markProjectionDoneSoon(next, effects);
    }
    return { state: next, effects };
  }
  return markProjectionDoneSoon(
    {
      ...state,
      projection: projectCommandResult(state.projection, prompt, command.text ?? ""),
    },
    [],
  );
}

function markProjectionDoneSoon(
  state: SessionReadState,
  effects: SessionReadEffect[],
): SessionReadResult {
  return {
    state: { ...state, projection: markProjectionDone(state.projection) },
    effects: [...effects, { type: "scheduleIdleStatus" }],
  };
}

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  if (typeof error === "string" && error.trim()) {
    return error;
  }
  return "Failed to load conversation.";
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
