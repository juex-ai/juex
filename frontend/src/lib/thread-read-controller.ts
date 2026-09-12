import {
  clearComposerHint,
  createThreadReadState,
  projectComposerHint,
  projectLiveBrowserEvent,
  projectLoadOlderFailed,
  projectLoadOlderStarted,
  projectLoadOlderSucceeded,
  projectPendingSubmit,
  projectPromptInputChanged,
  projectThreadLoadFailed,
  projectThreadLoaded,
  projectStartTurnFailed,
  projectStartTurnSucceeded,
  resetThreadReadState,
  type ThreadLiveSubscription,
  type ThreadReadEffect,
  type ThreadReadResult,
  type ThreadReadState,
} from "./thread-read-state.ts";
import { isCompactCommandInput } from "./compact-ui.ts";
import type {
  AgentRuntimeStatusSnapshot,
  BrowserEvent,
  MediaRef,
  ThreadShowResponse,
  StartTurnResponse,
} from "../types.ts";

export type ThreadReadRouteSnapshot = {
  id: string;
};

export type ThreadReadRefreshOptions = {
  preserveLiveMessages?: boolean;
  preserveLoadedHistory?: boolean;
  recordLoadFailure?: boolean;
};

type TimerHandle = ReturnType<typeof setTimeout>;

type ThreadReadSubscribeEvents = (
  id: string,
  opts: {
    since?: string;
    onEvent: (event: BrowserEvent) => void;
    onOpen?: () => void;
    onError?: (event: Event) => void;
  },
) => () => void;

type ThreadReadLiveOptions = {
  since?: string;
};

export type ThreadReadControllerLiveStatus = {
  load: (threadID: string) => Promise<AgentRuntimeStatusSnapshot>;
  apply: (
    threadID: string,
    status: AgentRuntimeStatusSnapshot,
  ) => void;
  clear: (threadID: string) => void;
  onRefreshError?: (error: unknown) => void;
  onStreamError?: (event: Event) => void;
};

export type ThreadReadControllerPorts = {
  initialState?: ThreadReadState;
  onStateChange: (state: ThreadReadState) => void;
  getThread: (
    id: string,
    opts?: { before?: string; limit?: number },
  ) => Promise<ThreadShowResponse>;
  startTurn: (
    id: string,
    prompt: string,
    attachments?: MediaRef[],
  ) => Promise<StartTurnResponse>;
  subscribeEvents: ThreadReadSubscribeEvents;
  setTimeout?: (callback: () => void, ms: number) => TimerHandle;
  clearTimeout?: (handle: TimerHandle) => void;
  logError?: (message: string, error: unknown) => void;
};

export type ThreadReadController = ReturnType<typeof createThreadReadController>;

const COMPOSER_HINT_DELAY_MS = 1800;

export function isLatestThreadRoute(
  latest: ThreadReadRouteSnapshot,
  id: string,
): boolean {
  return latest.id === id;
}

export function createThreadReadController(ports: ThreadReadControllerPorts) {
  let state = ports.initialState ?? createThreadReadState();
  let route: ThreadReadRouteSnapshot = { id: "" };
  let liveStatus: ThreadReadControllerLiveStatus | null = null;
  let liveResumeCursor: ThreadLiveSubscription | null = null;
  let composerHintTimer: TimerHandle | null = null;

  const setTimer = ports.setTimeout ?? setTimeout;
  const clearTimer = ports.clearTimeout ?? clearTimeout;

  function currentState(): ThreadReadState {
    return state;
  }

  function currentRoute(): ThreadReadRouteSnapshot {
    return route;
  }

  function configureLiveStatus(
    next: ThreadReadControllerLiveStatus | null,
  ) {
    liveStatus = next;
  }

  function setRoute(id: string) {
    if (route.id !== id) {
      liveResumeCursor = null;
    }
    route = { id };
  }

  function resetForRoute() {
    clearTransientTimers();
    liveResumeCursor = null;
    setThreadReadState(resetThreadReadState(state));
  }

  function setThreadReadState(next: ThreadReadState) {
    state = next;
    ports.onStateChange(next);
  }

  function updateReadState(project: (state: ThreadReadState) => ThreadReadState) {
    setThreadReadState(project(state));
  }

  function runThreadReadResult(result: ThreadReadResult) {
    setThreadReadState(result.state);
    runThreadReadEffects(result.effects);
  }

  function runThreadReadEffects(effects: ThreadReadEffect[]) {
    for (const effect of effects) {
      if (effect.type === "refresh") {
        void refresh(route.id, {
          preserveLiveMessages: effect.preserveLiveMessages,
          preserveLoadedHistory: effect.preserveLoadedHistory,
        });
        continue;
      }
      if (effect.type === "scheduleComposerHintClear") {
        scheduleComposerHintClear();
        continue;
      }
    }
  }

  let historyRevision = 0;

  async function refresh(
    threadID = route.id,
    opts: ThreadReadRefreshOptions = {},
  ) {
    if (!threadID) return;
    const revision = ++historyRevision;
    try {
      const next = await ports.getThread(threadID);
      if (!isLatestThreadRoute(route, threadID) || revision !== historyRevision) return;
      updateReadState((prev) => projectThreadLoaded(prev, next, opts));
    } catch (error) {
      if (!isLatestThreadRoute(route, threadID) || revision !== historyRevision) return;
      logError("getThread failed", error);
      if (opts.recordLoadFailure) {
        updateReadState((prev) => projectThreadLoadFailed(prev, error));
      }
    }
  }

  function subscribeLiveEvents(
    threadID = route.id,
    opts: ThreadReadLiveOptions = {},
  ) {
    let subscribed = true;
    const status = liveStatus;
    let statusRevision = 0;
    let refreshGeneration = 0;
    const refreshStatus = async () => {
      if (!status) return;
      const generation = ++refreshGeneration;
      const revision = statusRevision;
      try {
        const snapshot = await status.load(threadID);
        if (
          !subscribed ||
          !isLatestThreadRoute(route, threadID) ||
          generation !== refreshGeneration ||
          revision !== statusRevision
        ) {
          return;
        }
        statusRevision += 1;
        status.apply(threadID, snapshot);
      } catch (error) {
        if (
          !subscribed ||
          !isLatestThreadRoute(route, threadID) ||
          generation !== refreshGeneration ||
          revision !== statusRevision
        ) {
          return;
        }
        status.clear(threadID);
        status.onRefreshError?.(error);
      }
    };
    const resumeSince =
      liveResumeCursor?.threadID === threadID
        ? liveResumeCursor.cursor
        : opts.since;
    const unsubscribe = ports.subscribeEvents(threadID, {
      since: resumeSince,
      onEvent: (event) => {
        if (!subscribed || !isLatestThreadRoute(route, threadID)) return;
        statusRevision += 1;
        if (status) {
          status.apply(threadID, event.status);
        }
        runThreadReadResult(projectLiveBrowserEvent(state, event));
        const cursor = event.status.cursor?.trim();
        if (cursor) {
          liveResumeCursor = { threadID, cursor };
        }
      },
      onOpen: () => {
        void refreshStatus();
        // Restart can change the effective module composition while this page stays open.
        void refresh(threadID, { preserveLiveMessages: true, preserveLoadedHistory: true });
      },
      onError: (event) => {
        if (!subscribed || !isLatestThreadRoute(route, threadID)) return;
        statusRevision += 1;
        status?.clear(threadID);
        status?.onStreamError?.(event);
      },
    });
    void refreshStatus();
    return () => {
      subscribed = false;
      refreshGeneration += 1;
      unsubscribe();
      if (status) {
        status.clear(threadID);
      }
      clearTransientTimers();
    };
  }

  async function loadOlderMessages(threadID: string, before?: string) {
    if (!before || state.loadingOlderMessages) return;
    updateReadState(projectLoadOlderStarted);
    try {
      const page = await ports.getThread(threadID, { before });
      if (!isLatestThreadRoute(route, threadID)) return;
      updateReadState((prev) => projectLoadOlderSucceeded(prev, page));
    } catch (error) {
      if (!isLatestThreadRoute(route, threadID)) return;
      updateReadState((prev) => projectLoadOlderFailed(prev, error));
    }
  }

  async function submitPrompt(
    threadID: string,
    prompt: string,
    attachments: MediaRef[] = [],
  ): Promise<boolean> {
    if (!isLatestThreadRoute(route, threadID)) return false;
    const submittedAt = new Date().toISOString();
    const compactCommand = isCompactCommandInput(prompt);
    updateReadState((prev) =>
      projectPendingSubmit(prev, prompt, submittedAt),
    );
    try {
      // Acceptance belongs to the submitted Input even after its view unmounts.
      const turn = await ports.startTurn(threadID, prompt, attachments);
      if (!isLatestThreadRoute(route, threadID)) return true;
      runThreadReadResult(
        projectStartTurnSucceeded(
          state,
          prompt,
          turn,
          attachments,
          submittedAt,
        ),
      );
      return true;
    } catch (error) {
      if (!isLatestThreadRoute(route, threadID)) return false;
      logError("startTurn failed", error);
      runThreadReadResult(projectStartTurnFailed(state, compactCommand, error));
      return false;
    }
  }

  function projectPromptInput() {
    updateReadState(projectPromptInputChanged);
  }

  function showComposerHint(message: string) {
    runThreadReadResult(projectComposerHint(state, message));
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
    configureLiveStatus,
    currentRoute,
    currentState,
    loadOlderMessages,
    projectPromptInput,
    refresh,
    resetForRoute,
    runThreadReadResult,
    setRoute,
    showComposerHint,
    submitPrompt,
    subscribeLiveEvents,
    dispose: clearTransientTimers,
  };
}
