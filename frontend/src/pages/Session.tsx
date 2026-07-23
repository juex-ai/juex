import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ChevronUpIcon, LoaderCircleIcon } from "lucide-react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { useStickToBottomContext } from "use-stick-to-bottom";

import {
  getSession,
  getSessionContext,
  getSessionStatus,
  interrupt,
  startTurn,
  subscribeEvents,
  uploadSessionAttachment,
} from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation";
import {
  useAgentSessionStatus,
  useFleetAgent,
} from "@/components/fleet/FleetAgentContext";
import { SessionComposer } from "@/components/session/SessionComposer";
import { SessionTranscript } from "@/components/session/SessionTranscript";
import { Button } from "@/components/ui/button";
import {
  assistantWorkItems,
  transcriptItemModelLabels,
} from "@/lib/assistant-work-groups";
import { messagesToGroups } from "@/lib/display-units";
import { agentPathFromLocation } from "@/lib/fleet-routes";
import {
  createSessionReadController,
  type SessionReadController,
} from "@/lib/session-read-controller";
import {
  captureSessionLiveSubscription,
  createSessionReadState,
  type SessionInitialCommandState,
  type SessionLiveSubscription,
  type SessionReadState,
} from "@/lib/session-read-state";
import { sessionCanSend } from "@/lib/session-access";
import { sessionPreviewTitle } from "@/lib/session-title";
import type { Message as ChatMessage } from "@/types";

type InitialCommandState = SessionInitialCommandState;

export function Session() {
  const { id = "" } = useParams<{ id: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const { agent, agentsLoaded, statusStore } = useFleetAgent();
  const runtimeStatus = useAgentSessionStatus(agent?.id, id);
  const [readState, setReadState] = useState<SessionReadState>(() =>
    createSessionReadState(),
  );
  const controller = useMemo<SessionReadController>(
    () =>
      createSessionReadController({
        initialState: createSessionReadState(),
        onStateChange: setReadState,
        getSession,
        getSessionContext,
        startTurn,
        subscribeEvents,
        logError: (message, error) => console.error(message, error),
      }),
    [],
  );
  const [sessionLiveSubscription, setSessionLiveSubscription] =
    useState<SessionLiveSubscription | null>(null);
  const [composerClearance, setComposerClearance] = useState(0);
  const {
    data,
    loadError,
    projection,
    activeContext,
    composerHint,
    submitError,
    loadingOlderMessages,
    olderMessagesError,
  } = readState;
  const agentRuntimeHealthy =
    !agentsLoaded || agent?.runtime_health === "healthy";

  useEffect(() => {
    controller.configureNavigation({
      clearRouteState: () =>
        navigate(location.pathname, { replace: true, state: null }),
      dispatchSessionsChanged: () =>
        window.dispatchEvent(new Event("juex:sessions-changed")),
      navigateToSession: (sessionID, state) =>
        navigate(
          agentPathFromLocation(
            `/sessions/${encodeURIComponent(sessionID)}`,
            location.pathname,
          ),
          { state },
        ),
    });
  }, [controller, location.pathname, navigate]);

  useEffect(() => {
    return () => controller.dispose();
  }, [controller]);

  useEffect(() => {
    controller.setRoute(id);
    controller.resetForRoute();
    setSessionLiveSubscription(null);
  }, [controller, id]);

  useEffect(() => {
    if (!id) return;
    void controller.refresh(id, {
      preserveLiveMessages: true,
      recordLoadFailure: true,
    });
  }, [controller, id]);

  useEffect(() => {
    if (!data || data.id !== id) return;
    setSessionLiveSubscription((current) =>
      captureSessionLiveSubscription(current, data),
    );
  }, [data, id]);

  const canSubscribeLiveSession = data ? sessionCanSend(data) : false;

  useEffect(() => {
    if (
      !id ||
      sessionLiveSubscription?.sessionID !== id ||
      !agent?.id ||
      !statusStore ||
      !agentRuntimeHealthy ||
      !canSubscribeLiveSession
    ) {
      controller.configureLiveStatus(null);
      return;
    }
    const agentID = agent.id;
    controller.configureLiveStatus({
      load: getSessionStatus,
      apply: (_sessionID, status) => statusStore.setStatus(agentID, status),
      clear: (sessionID) => statusStore.clearStatus(agentID, sessionID),
      onRefreshError: (error) =>
        console.error("refresh session status failed", error),
      onStreamError: (event) =>
        console.error("session event stream failed", event),
    });
    const unsubscribe = controller.subscribeLiveEvents(id, {
      since: sessionLiveSubscription.cursor,
    });
    return () => {
      unsubscribe();
      controller.configureLiveStatus(null);
    };
  }, [
    agent?.id,
    agentRuntimeHealthy,
    canSubscribeLiveSession,
    controller,
    id,
    sessionLiveSubscription,
    statusStore,
  ]);

  useEffect(() => {
    if (!data) return;
    const state = location.state as InitialCommandState;
    if (!state?.command || !state.commandInput) return;
    controller.projectInitialCommandOnce(id, state.commandInput, state.command);
  }, [controller, data, id, location.state]);

  useShellTitle(
    data ? sessionPreviewTitle(data.preview) : null,
    data?.last_active_at ?? null,
  );

  const messages = useMemo<ChatMessage[]>(
    () => [...(data?.messages ?? []), ...projection.messages],
    [data?.messages, projection.messages],
  );
  const groups = useMemo(
    () =>
      messagesToGroups(messages, runtimeStatus?.tools, {
        runtimeStatusLoaded: runtimeStatus !== undefined,
        activeTurnID:
          runtimeStatus?.turn?.state === "admitted" ||
          runtimeStatus?.turn?.state === "active"
            ? runtimeStatus.turn.id
            : undefined,
      }),
    [messages, runtimeStatus],
  );
  const runtimeTurnState = runtimeStatus?.turn?.state;
  const transcriptItems = useMemo(
    () =>
      assistantWorkItems(groups, {
        tailActive:
          runtimeTurnState === "admitted" || runtimeTurnState === "active",
      }),
    [groups, runtimeTurnState],
  );
  const modelLabels = useMemo(
    () => transcriptItemModelLabels(transcriptItems),
    [transcriptItems],
  );

  if (!data) {
    if (loadError) {
      return (
        <SessionLoadErrorState
          detail={loadError}
          onHistory={() =>
            navigate(agentPathFromLocation("/history", location.pathname))
          }
        />
      );
    }
    return <LoadingState label="Loading conversation" />;
  }

  const canSend = sessionCanSend(data) && agentRuntimeHealthy;
  const effectiveClearance = canSend ? composerClearance : 0;

  return (
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
      <Conversation className="min-h-0 flex-1">
        <ConversationClearanceFollower clearance={effectiveClearance} />
        <ConversationContent
          className="mx-auto w-full max-w-[808px]"
          style={{ paddingBottom: effectiveClearance || undefined }}
        >
          {data.has_more_before ||
          loadingOlderMessages ||
          olderMessagesError ? (
            <LoadOlderMessagesControl
              disabled={loadingOlderMessages || !data.oldest_message_id}
              error={olderMessagesError}
              loading={loadingOlderMessages}
              onLoad={() =>
                void controller.loadOlderMessages(
                  id,
                  data.oldest_message_id,
                )
              }
            />
          ) : null}
          <SessionTranscript
            compactCommandInputs={projection.compactCommandInputs}
            items={transcriptItems}
            modelLabels={modelLabels}
          />
        </ConversationContent>
        <ConversationScrollButton
          className="z-30"
          style={{ bottom: effectiveClearance ? effectiveClearance + 16 : 16 }}
        />
      </Conversation>
      <SessionComposer
        key={id}
        activeContext={activeContext}
        agentRuntimeHealthy={agentRuntimeHealthy}
        canSend={canSend}
        composerHint={composerHint}
        data={data}
        onClearanceChange={setComposerClearance}
        onInterrupt={() => void handleInterrupt()}
        onPromptInput={controller.projectPromptInput}
        onSend={(prompt, attachments) =>
          controller.submitPrompt(id, prompt, attachments)
        }
        onShowHint={controller.showComposerHint}
        onUploadAttachment={(file) => uploadSessionAttachment(id, file)}
        queuedInputs={projection.queuedInput.items}
        runtimeStatus={runtimeStatus}
        submitError={submitError}
      />
    </div>
  );

  async function handleInterrupt() {
    try {
      await interrupt(id);
    } catch (error) {
      console.error("interrupt failed", error);
    }
  }
}

function ConversationClearanceFollower({
  clearance,
}: {
  clearance: number;
}) {
  const { isAtBottom, scrollToBottom } = useStickToBottomContext();
  const previousClearance = useRef(clearance);

  useLayoutEffect(() => {
    const grew = clearance > previousClearance.current;
    previousClearance.current = clearance;
    if (!grew || !isAtBottom) return;
    void scrollToBottom({ animation: "instant" });
  }, [clearance, isAtBottom, scrollToBottom]);

  return null;
}

function SessionLoadErrorState({
  detail,
  onHistory,
}: {
  detail: string;
  onHistory: () => void;
}) {
  return (
    <div
      className="flex min-h-0 flex-1 items-center justify-center bg-background px-4 py-8 text-center"
      role="alert"
    >
      <div className="flex max-w-md flex-col items-center gap-3">
        <div className="font-serif text-2xl italic text-primary">
          Conversation unavailable
        </div>
        <p className="break-words font-mono text-xs text-muted-foreground">
          {detail}
        </p>
        <Button
          className="mt-1 h-8 rounded-full px-3 font-mono text-[11px]"
          onClick={onHistory}
          type="button"
          variant="outline"
        >
          Open history
        </Button>
      </div>
    </div>
  );
}

function LoadOlderMessagesControl({
  disabled,
  error,
  loading,
  onLoad,
}: {
  disabled: boolean;
  error: string | null;
  loading: boolean;
  onLoad: () => void;
}) {
  return (
    <div className="flex flex-col items-center gap-2 py-1">
      <Button
        className="h-8 rounded-full px-3 font-mono text-[11px]"
        disabled={disabled}
        onClick={onLoad}
        type="button"
        variant="outline"
      >
        {loading ? (
          <LoaderCircleIcon
            className="size-3.5 animate-spin motion-reduce:animate-none"
            aria-hidden="true"
          />
        ) : (
          <ChevronUpIcon className="size-3.5" aria-hidden="true" />
        )}
        Load older messages
      </Button>
      {error ? (
        <div className="max-w-full truncate font-mono text-[11px] text-juex-error">
          {error}
        </div>
      ) : null}
    </div>
  );
}
