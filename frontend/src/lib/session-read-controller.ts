import {
  clearComposerHint,
  createSessionReadState,
  projectActiveContextFailed,
  projectActiveContextLoaded,
  projectComposerHint,
  projectInitialCommand,
  projectLiveBrowserEvent,
  projectLoadOlderFailed,
  projectLoadOlderStarted,
  projectLoadOlderSucceeded,
  projectPendingSubmit,
  projectPromptInputChanged,
  projectSessionLoadFailed,
  projectSessionLoaded,
  projectStartTurnFailed,
  projectStartTurnSucceeded,
  resetSessionReadState,
  type SessionInitialCommandState,
  type SessionReadEffect,
  type SessionReadResult,
  type SessionReadState,
} from "./session-read-state.ts";
import { isCompactCommandInput } from "./compact-ui.ts";
import type {
  ActiveContextSnapshot,
  AgentRuntimeStatusSnapshot,
  BrowserEvent,
  MediaRef,
  SessionShowResponse,
  SlashCommandResponse,
  StartTurnResponse,
} from "../types.ts";

export type SessionReadRouteSnapshot = {
  id: string;
};

export type SessionReadRefreshOptions = {
  preserveLiveMessages?: boolean;
  recordLoadFailure?: boolean;
};

type TimerHandle = ReturnType<typeof setTimeout>;

type SessionReadSubscribeEvents = (
  id: string,
  opts: {
    since?: string;
    onEvent: (event: BrowserEvent) => void;
    onOpen?: () => void;
    onError?: (event: Event) => void;
  },
) => () => void;

type SessionReadLiveOptions = {
  since?: string;
  loadStatus?: () => Promise<AgentRuntimeStatusSnapshot>;
  onStatus?: (status: AgentRuntimeStatusSnapshot) => void;
  onStatusRefreshError?: (error: unknown) => void;
  onError?: (event: Event) => void;
};

export type SessionReadControllerNavigation = {
  clearRouteState: () => void;
  dispatchSessionsChanged: () => void;
  navigateToSession: (
    sessionID: string,
    state: SessionInitialCommandState,
  ) => void;
};

export type SessionReadControllerPorts = {
  initialState?: SessionReadState;
  onStateChange: (state: SessionReadState) => void;
  getSession: (
    id: string,
    opts?: { before?: string; limit?: number },
  ) => Promise<SessionShowResponse>;
  getSessionContext: (id: string) => Promise<ActiveContextSnapshot>;
  startTurn: (
    id: string,
    prompt: string,
    attachments?: MediaRef[],
  ) => Promise<StartTurnResponse>;
  subscribeEvents: SessionReadSubscribeEvents;
  setTimeout?: (callback: () => void, ms: number) => TimerHandle;
  clearTimeout?: (handle: TimerHandle) => void;
  navigation?: Partial<SessionReadControllerNavigation>;
  logError?: (message: string, error: unknown) => void;
};

export type SessionReadController = ReturnType<typeof createSessionReadController>;

const COMPOSER_HINT_DELAY_MS = 1800;

const noopNavigation: SessionReadControllerNavigation = {
  clearRouteState: () => {},
  dispatchSessionsChanged: () => {},
  navigateToSession: () => {},
};

export function isLatestSessionRoute(
  latest: SessionReadRouteSnapshot,
  id: string,
): boolean {
  return latest.id === id;
}

export function createSessionReadController(ports: SessionReadControllerPorts) {
  let state = ports.initialState ?? createSessionReadState();
  let route: SessionReadRouteSnapshot = { id: "" };
  let navigation: SessionReadControllerNavigation = {
    ...noopNavigation,
    ...ports.navigation,
  };
  let composerHintTimer: TimerHandle | null = null;
  let initialCommandKey: string | null = null;

  const setTimer = ports.setTimeout ?? setTimeout;
  const clearTimer = ports.clearTimeout ?? clearTimeout;

  function currentState(): SessionReadState {
    return state;
  }

  function currentRoute(): SessionReadRouteSnapshot {
    return route;
  }

  function configureNavigation(
    next: Partial<SessionReadControllerNavigation>,
  ) {
    navigation = { ...navigation, ...next };
  }

  function setRoute(id: string) {
    route = { id };
  }

  function resetForRoute() {
    clearTransientTimers();
    initialCommandKey = null;
    setSessionReadState(resetSessionReadState(state));
  }

  function setSessionReadState(next: SessionReadState) {
    state = next;
    ports.onStateChange(next);
  }

  function updateReadState(project: (state: SessionReadState) => SessionReadState) {
    setSessionReadState(project(state));
  }

  function runSessionReadResult(result: SessionReadResult) {
    setSessionReadState(result.state);
    runSessionReadEffects(result.effects);
  }

  function runSessionReadEffects(effects: SessionReadEffect[]) {
    for (const effect of effects) {
      if (effect.type === "refresh") {
        void refresh(route.id, {
          preserveLiveMessages: effect.preserveLiveMessages,
        });
        continue;
      }
      if (effect.type === "scheduleComposerHintClear") {
        scheduleComposerHintClear();
        continue;
      }
      if (effect.type === "clearRouteState") {
        navigation.clearRouteState();
        continue;
      }
      if (effect.type === "dispatchSessionsChanged") {
        navigation.dispatchSessionsChanged();
        continue;
      }
      if (effect.type === "navigateToSession") {
        navigation.navigateToSession(effect.sessionID, effect.state);
        continue;
      }
    }
  }

  async function refresh(
    sessionID = route.id,
    opts: SessionReadRefreshOptions = {},
  ) {
    if (!sessionID) return;
    try {
      const next = await ports.getSession(sessionID);
      if (!isLatestSessionRoute(route, sessionID)) return;
      updateReadState((prev) => projectSessionLoaded(prev, next, opts));
      await refreshActiveContext(sessionID);
    } catch (error) {
      if (!isLatestSessionRoute(route, sessionID)) return;
      logError("getSession failed", error);
      if (opts.recordLoadFailure) {
        updateReadState((prev) => projectSessionLoadFailed(prev, error));
      }
    }
  }

  async function refreshActiveContext(sessionID = route.id) {
    if (!sessionID) return;
    try {
      const context = await ports.getSessionContext(sessionID);
      if (!isLatestSessionRoute(route, sessionID)) return;
      updateReadState((prev) => projectActiveContextLoaded(prev, context));
    } catch (error) {
      if (!isLatestSessionRoute(route, sessionID)) return;
      logError("getSessionContext failed", error);
      updateReadState(projectActiveContextFailed);
    }
  }

  function subscribeLiveEvents(
    sessionID = route.id,
    opts: SessionReadLiveOptions = {},
  ) {
    let subscribed = true;
    let statusRevision = 0;
    let refreshGeneration = 0;
    const refreshStatus = async () => {
      if (!opts.loadStatus || !opts.onStatus) return;
      const generation = ++refreshGeneration;
      const revision = statusRevision;
      try {
        const status = await opts.loadStatus();
        if (
          !subscribed ||
          !isLatestSessionRoute(route, sessionID) ||
          generation !== refreshGeneration ||
          revision !== statusRevision
        ) {
          return;
        }
        statusRevision += 1;
        opts.onStatus(status);
      } catch (error) {
        if (!subscribed || generation !== refreshGeneration) return;
        opts.onStatusRefreshError?.(error);
      }
    };
    const unsubscribe = ports.subscribeEvents(sessionID, {
      since: opts.since,
      onEvent: (event) => {
        if (!subscribed || !isLatestSessionRoute(route, sessionID)) return;
        statusRevision += 1;
        opts.onStatus?.(event.status);
        runSessionReadResult(projectLiveBrowserEvent(state, event));
      },
      onOpen: () => {
        void refreshStatus();
      },
      onError: opts.onError,
    });
    return () => {
      subscribed = false;
      refreshGeneration += 1;
      unsubscribe();
      clearTransientTimers();
    };
  }

  async function loadOlderMessages(sessionID: string, before?: string) {
    if (!before || state.loadingOlderMessages) return;
    updateReadState(projectLoadOlderStarted);
    try {
      const page = await ports.getSession(sessionID, { before });
      if (!isLatestSessionRoute(route, sessionID)) return;
      updateReadState((prev) => projectLoadOlderSucceeded(prev, page));
    } catch (error) {
      if (!isLatestSessionRoute(route, sessionID)) return;
      updateReadState((prev) => projectLoadOlderFailed(prev, error));
    }
  }

  async function submitPrompt(
    sessionID: string,
    prompt: string,
    attachments: MediaRef[] = [],
  ): Promise<boolean> {
    if (!isLatestSessionRoute(route, sessionID)) return false;
    const compactCommand = isCompactCommandInput(prompt);
    updateReadState((prev) => projectPendingSubmit(prev, prompt));
    try {
      const turn = await ports.startTurn(sessionID, prompt, attachments);
      if (!isLatestSessionRoute(route, sessionID)) return false;
      runSessionReadResult(
        projectStartTurnSucceeded(state, prompt, turn, attachments),
      );
      return true;
    } catch (error) {
      if (!isLatestSessionRoute(route, sessionID)) return false;
      logError("startTurn failed", error);
      runSessionReadResult(projectStartTurnFailed(state, compactCommand, error));
      return false;
    }
  }

  function projectInitialCommandOnce(
    sessionID: string,
    commandInput: string,
    command: SlashCommandResponse,
  ) {
    const key = `${sessionID}:${commandInput}:${command.name}:${command.text}`;
    if (initialCommandKey === key) return;
    initialCommandKey = key;
    runSessionReadResult(projectInitialCommand(state, commandInput, command));
  }

  function projectPromptInput() {
    updateReadState(projectPromptInputChanged);
  }

  function showComposerHint(message: string) {
    runSessionReadResult(projectComposerHint(state, message));
  }

  function scheduleComposerHintClear() {
    if (composerHintTimer !== null) {
      clearTimer(composerHintTimer);
    }
    composerHintTimer = setTimer(() => updateReadState(clearComposerHint), COMPOSER_HINT_DELAY_MS);
  }

  function clearTransientTimers() {
    if (composerHintTimer !== null) {
      clearTimer(composerHintTimer);
      composerHintTimer = null;
    }
  }

  function logError(message: string, error: unknown) {
    ports.logError?.(message, error);
  }

  return {
    configureNavigation,
    currentRoute,
    currentState,
    loadOlderMessages,
    projectInitialCommandOnce,
    projectPromptInput,
    refresh,
    refreshActiveContext,
    resetForRoute,
    runSessionReadResult,
    setRoute,
    showComposerHint,
    submitPrompt,
    subscribeLiveEvents,
    dispose: clearTransientTimers,
  };
}
