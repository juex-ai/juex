import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Button } from "@/components/ui/button";
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation";
import {
  Message,
  MessageAction,
  MessageActions,
  MessageContent,
  MessageResponse,
} from "@/components/ai-elements/message";
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from "@/components/ai-elements/reasoning";
import {
  Tool,
  ToolContent,
  ToolHeader,
  ToolInput,
  ToolOutput,
} from "@/components/ai-elements/tool";
import {
  PromptInput,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
} from "@/components/ai-elements/prompt-input";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import {
  messagesToGroups,
  toolState,
  type MessageGroup,
} from "@/lib/display-units";
import { assistantBlocksFromEventPayload } from "@/lib/assistant-blocks";
import {
  applyToolOutputDeltaToMessages,
  applyToolRequestedToMessages,
} from "@/lib/live-tool-events";
import {
  COMPACTING_SUBMIT_HINT,
  composerSubmitAction,
  type ComposerSubmitAction,
} from "@/lib/composer-submit";
import {
  isCompactCommandInput,
  isLocalCompactMessage,
  LOCAL_COMPACT_COMMAND_ID,
  LOCAL_COMPACT_PENDING_ID,
  LOCAL_COMPACT_PENDING_KIND,
  PENDING_COMPACT_LABEL,
} from "@/lib/compact-ui";
import { writeClipboardText } from "@/lib/clipboard";
import {
  COMPACT_COPIED_TOOLTIP,
  compactSummaryText,
  copyButtonDefaultTooltipMode,
  copyButtonTooltip,
  messageGroupCanCopy,
  messageGroupCopyText,
  type CopyTooltipMode,
} from "@/lib/message-copy";
import { sessionPreviewTitle } from "@/lib/session-title";
import { mergeOlderSessionPage } from "@/lib/session-messages";
import { formatMCPEventForDisplay } from "@/lib/mcp-events";
import { cn } from "@/lib/utils";
import { QueuedInputStack } from "@/components/QueuedInputStack";
import { Separator } from "@/components/ui/separator";
import {
  createQueuedInputState,
  drainQueuedInputs as drainQueuedInputState,
  dropQueuedInputs as dropQueuedInputState,
  enqueueQueuedInput as enqueueQueuedInputState,
  type QueuedInput,
  type QueuedInputState,
} from "@/lib/queued-inputs";
import {
  getSession,
  getSessionContext,
  getTurnStatus,
  interrupt,
  startTurn,
  subscribeEvents,
} from "@/api";
import { sessionCanSend, sessionReadOnlyMessage } from "@/lib/session-access";
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  CopyIcon,
  LoaderCircleIcon,
  RadioIcon,
  SendHorizontalIcon,
  SquareIcon,
} from "lucide-react";
import type {
  ActiveContextSnapshot,
  ContextUsage,
  Message as ChatMessage,
  SessionShowResponse,
  SlashCommandResponse,
  TokenUsage,
} from "@/types";

type InitialCommandState = {
  activeTurnID?: string;
  commandInput?: string;
  command?: SlashCommandResponse;
} | null;

type SessionStatus =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "pending"; count: number }
  | { kind: "tool"; name: string }
  | { kind: "done" }
  | { kind: "error"; detail?: string };

type SessionRouteSnapshot = {
  id: string;
};

function isLatestRoute(latest: SessionRouteSnapshot, id: string): boolean {
  return latest.id === id;
}

export function Session() {
  const { id = "" } = useParams<{ id: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const [data, setData] = useState<SessionShowResponse | null>(null);
  const [liveMessages, setLiveMessages] = useState<ChatMessage[]>([]);
  const [liveTokenUsage, setLiveTokenUsage] = useState<TokenUsage | null>(null);
  const [liveContextUsage, setLiveContextUsage] =
    useState<ContextUsage | null>(null);
  const [activeContext, setActiveContext] =
    useState<ActiveContextSnapshot | null>(null);
  const [status, setStatus] = useState<SessionStatus>({ kind: "idle" });
  const [turnActive, setTurnActive] = useState(false);
  const [compactActive, setCompactActive] = useState(false);
  const [draft, setDraft] = useState("");
  const [composerHint, setComposerHint] = useState<string | null>(null);
  const [loadingOlderMessages, setLoadingOlderMessages] = useState(false);
  const [olderMessagesError, setOlderMessagesError] = useState<string | null>(
    null,
  );
  const [compactCommandInputs, setCompactCommandInputs] = useState<
    Record<string, string>
  >({});
  const [queuedInputState, setQueuedInputState] = useState<QueuedInputState>(
    () => createQueuedInputState(),
  );
  const doneTimerRef = useRef<number | null>(null);
  const composerHintTimerRef = useRef<number | null>(null);
  const initialCommandRef = useRef<string | null>(null);
  const turnActiveRef = useRef(false);
  const queuedInputStateRef = useRef<QueuedInputState>(
    createQueuedInputState(),
  );
  const latestRouteRef = useRef({ id });

  useEffect(() => {
    latestRouteRef.current = { id };
  }, [id]);

  // refresh is stable per route mode and session id.
  const refresh = useCallback(async (
    opts?: { preserveLiveMessages?: boolean },
  ) => {
    const requestId = id;
    try {
      const r = await getSession(requestId);
      if (!isLatestRoute(latestRouteRef.current, requestId)) {
        return;
      }
      setData(r);
      setOlderMessagesError(null);
      if (!opts?.preserveLiveMessages) setLiveMessages([]);
      setLiveTokenUsage(null);
      setLiveContextUsage(null);
      try {
        const context = await getSessionContext(requestId);
        if (!isLatestRoute(latestRouteRef.current, requestId)) {
          return;
        }
        setActiveContext(context);
      } catch (contextError) {
        if (isLatestRoute(latestRouteRef.current, requestId)) {
          console.error("getSessionContext failed", contextError);
          setActiveContext(null);
        }
      }
    } catch (e) {
      if (isLatestRoute(latestRouteRef.current, requestId)) {
        console.error("getSession failed", e);
      }
    }
  }, [id]);

  useEffect(() => {
    const next = createQueuedInputState();
    queuedInputStateRef.current = next;
    setQueuedInputState(next);
    const state = location.state as InitialCommandState;
    const activeTurnID = state?.activeTurnID;
    setTurnActiveControllerState(Boolean(activeTurnID));
    setStatus(activeTurnID ? { kind: "running" } : { kind: "idle" });
    setCompactActiveControllerState(false);
    setDraft("");
    setComposerHint(null);
    setLoadingOlderMessages(false);
    setOlderMessagesError(null);
    setCompactCommandInputs({});
    if (!activeTurnID) return;

    let cancelled = false;
    let timer: number | null = null;
    const reconcile = async () => {
      try {
        const turn = await getTurnStatus(id, activeTurnID);
        if (cancelled || !isLatestRoute(latestRouteRef.current, id)) return;
        if (turn.state === "running") {
          setTurnActiveControllerState(true);
          setStatus(
            turn.pending_count && turn.pending_count > 0
              ? { kind: "pending", count: turn.pending_count }
              : { kind: "running" },
          );
          timer = window.setTimeout(() => void reconcile(), 1000);
          return;
        }

        await refresh();
        if (cancelled || !isLatestRoute(latestRouteRef.current, id)) return;
        if (queuedInputStateRef.current.items.length > 0) {
          const emptyQueue = createQueuedInputState();
          queuedInputStateRef.current = emptyQueue;
          setQueuedInputState(emptyQueue);
        }
        setTurnActiveControllerState(false);
        if (turn.state === "errored") {
          setStatus({ kind: "error", detail: turn.error });
        } else {
          markDoneSoon();
        }
      } catch (e) {
        if (!cancelled && isLatestRoute(latestRouteRef.current, id)) {
          console.error("getTurnStatus failed", e);
        }
      }
    };
    void reconcile();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [id, location.state, refresh]);

  useEffect(() => {
    if (!id) return;
    const requestId = id;
    let cancelled = false;
    (async () => {
      try {
        const r = await getSession(requestId);
        if (cancelled || !isLatestRoute(latestRouteRef.current, requestId)) {
          return;
        }
        setData(r);
        setOlderMessagesError(null);
        setLiveMessages([]);
        setLiveTokenUsage(null);
        setLiveContextUsage(null);
        try {
          const context = await getSessionContext(requestId);
          if (cancelled || !isLatestRoute(latestRouteRef.current, requestId)) {
            return;
          }
          setActiveContext(context);
        } catch (contextError) {
          if (!cancelled && isLatestRoute(latestRouteRef.current, requestId)) {
            console.error("getSessionContext failed", contextError);
            setActiveContext(null);
          }
        }
      } catch (e) {
        if (!cancelled && isLatestRoute(latestRouteRef.current, requestId)) {
          console.error("getSession failed", e);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [id]);

  useEffect(() => {
    if (!data) return;
    const state = location.state as InitialCommandState;
    if (!state?.command || !state.commandInput) return;
    const key = `${id}:${state.commandInput}:${state.command.name}:${state.command.text}`;
    if (initialCommandRef.current === key) return;
    initialCommandRef.current = key;
    if (state.command.name === "/compact" && state.command.compact?.message_id) {
      const messageID = state.command.compact.message_id;
      const commandInput = state.commandInput;
      void refresh().then(() => rememberCompactCommand(messageID, commandInput));
    } else {
      appendCommandResult(state.commandInput, state.command.text);
    }
    markDoneSoon();
    navigate(location.pathname, { replace: true, state: null });
  }, [
    data,
    id,
    location.pathname,
    location.state,
    navigate,
    refresh,
  ]);

  // SSE subscription.
  useEffect(() => {
    if (!id || !data || !sessionCanSend(data)) return;
    const unsub = subscribeEvents(id, {
      onEvent: (e) => {
        switch (e.type) {
          case "turn.started":
            consumeQueuedInput(eventInput(e), eventKind(e));
            appendLiveTurn(e.turn_id, eventInput(e), eventKind(e), "event");
            setTurnActiveControllerState(true);
            setStatus({ kind: "running" });
            break;
          case "llm.requested":
            setTurnActiveControllerState(true);
            setStatus({ kind: "running" });
            break;
          case "llm.responded":
            applyAssistantResponse(e);
            applyTokenUsage(e);
            applyContextUsage(e);
            setTurnActiveControllerState(true);
            setStatus({ kind: "running" });
            break;
          case "tool.requested": {
            const name =
              eventString(e, "name") ?? eventString(e, "tool_name") ?? "?";
            setLiveMessages((prev) =>
              applyToolRequestedToMessages(prev, {
                turnID: e.turn_id,
                toolUseID: eventString(e, "tool_use_id"),
                toolName: name,
                input: eventRecord(e, "input"),
                timeoutSeconds: eventNumberFromPayload(e, "timeout_seconds"),
              }),
            );
            setTurnActiveControllerState(true);
            setStatus({ kind: "tool", name });
            break;
          }
          case "tool.completed":
            appendToolResult(e, false);
            setTurnActiveControllerState(true);
            setStatus({ kind: "running" });
            break;
          case "tool.output_delta":
            setLiveMessages((prev) =>
              applyToolOutputDeltaToMessages(prev, {
                turnID: e.turn_id,
                toolUseID: eventString(e, "tool_use_id"),
                text: eventString(e, "text"),
              }),
            );
            setTurnActiveControllerState(true);
            setStatus({ kind: "tool", name: eventString(e, "name") ?? "exec_command" });
            break;
          case "tool.errored":
            appendToolResult(e, true);
            setTurnActiveControllerState(true);
            setStatus({ kind: "running" });
            break;
          case "pending_input.queued":
            enqueueQueuedInput(eventInput(e), eventKind(e), eventPendingCount(e));
            setTurnActiveControllerState(true);
            setStatus({ kind: "pending", count: eventPendingCount(e) });
            break;
          case "pending_input.drained":
            drainQueuedInputs(eventDeltaCount(e), e.turn_id);
            setTurnActiveControllerState(true);
            setStatus({ kind: "running" });
            break;
          case "pending_input.dropped":
            dropQueuedInputs(eventDeltaCount(e));
            setStatus({
              kind: "error",
              detail: `${eventDeltaCount(e)} pending input(s) dropped`,
            });
            break;
          case "pending_input.rejected":
            setTurnActiveControllerState(true);
            setStatus({
              kind: "error",
              detail: "pending input queue full",
            });
            break;
          case "turn.completed":
            refresh().then(() => {
              clearQueuedInputs();
              setTurnActiveControllerState(false);
              setStatus({ kind: "done" });
              if (doneTimerRef.current)
                window.clearTimeout(doneTimerRef.current);
              doneTimerRef.current = window.setTimeout(
                () => setStatus({ kind: "idle" }),
                1500,
              );
            });
            break;
          case "turn.errored":
            refresh().then(() => {
              clearQueuedInputs();
              setTurnActiveControllerState(false);
              setStatus({ kind: "error", detail: eventErrorDetail(e) });
            });
            break;
          case "context.compact.started":
            setCompactActiveControllerState(true);
            appendPendingCompact();
            setStatus({ kind: "running" });
            break;
          case "context.compact.completed":
            refresh({ preserveLiveMessages: true }).then(() => {
              clearLocalCompactMessages();
              setCompactActiveControllerState(false);
              if (
                !turnActiveRef.current &&
                queuedInputStateRef.current.items.length === 0
              ) {
                markDoneSoon();
              }
            });
            break;
          case "context.compact.errored":
            refresh({ preserveLiveMessages: true }).then(() => {
              clearLocalCompactMessages();
              setCompactActiveControllerState(false);
              setStatus({ kind: "error", detail: eventErrorDetail(e) });
            });
            break;
        }
      },
    });
    return () => {
      unsub();
      if (doneTimerRef.current) window.clearTimeout(doneTimerRef.current);
      if (composerHintTimerRef.current)
        window.clearTimeout(composerHintTimerRef.current);
    };
    // Queue helpers read from refs; resubscribing on every local queue change
    // would reopen the EventSource during active turns.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data?.kind, data?.active, id, refresh]);

  async function handleSend(prompt: string) {
    const compactCommand = isCompactCommandInput(prompt);
    if (compactCommand) {
      appendPendingCompact(prompt);
      setCompactActiveControllerState(true);
      setStatus({ kind: "running" });
    }
    try {
      const turn = await startTurn(id, prompt);
      if (turn.command) {
        if (turn.command.name === "/new" && turn.command.status?.session_id) {
          window.dispatchEvent(new Event("juex:sessions-changed"));
          navigate(
            `/sessions/${encodeURIComponent(turn.command.status.session_id)}`,
            {
              state: turn.turn_id
                ? { activeTurnID: turn.turn_id }
                : { commandInput: prompt, command: turn.command },
            },
          );
          return;
        }
        if (turn.command.name === "/compact") {
          await refresh({ preserveLiveMessages: true });
          clearLocalCompactMessages();
          setCompactActiveControllerState(false);
          if (turn.command.compact?.message_id) {
            rememberCompactCommand(turn.command.compact.message_id, prompt);
          } else {
            appendCommandResult(prompt, turn.command.text);
          }
          if (
            !turnActiveRef.current &&
            queuedInputStateRef.current.items.length === 0
          ) {
            markDoneSoon();
          }
          return;
        } else {
          appendCommandResult(prompt, turn.command.text);
        }
        markDoneSoon();
        return;
      }
      if (turn.queued) {
        enqueueQueuedInput(prompt, undefined, turn.pending_count ?? 0);
        setTurnActiveControllerState(true);
        setStatus({ kind: "pending", count: turn.pending_count ?? 0 });
      } else {
        if (!turn.turn_id) throw new Error("turn response missing turn_id");
        appendLiveTurn(turn.turn_id, prompt, undefined, "optimistic");
        setTurnActiveControllerState(true);
        setStatus({ kind: "running" });
      }
    } catch (e) {
      console.error("startTurn failed", e);
      if (compactCommand) {
        clearLocalCompactMessages();
        setCompactActiveControllerState(false);
      }
      setStatus({
        kind: "error",
        detail: e instanceof Error ? e.message : String(e),
      });
    }
  }

  async function handleInterrupt() {
    try {
      await interrupt(id);
    } catch (e) {
      console.error("interrupt failed", e);
    }
  }

  useShellTitle(
    data ? sessionPreviewTitle(data.preview) : null,
    data?.last_active_at ?? null,
  );

  if (!data) {
    return <LoadingState label="Loading conversation" />;
  }

  const messages: ChatMessage[] = [...(data.messages ?? []), ...liveMessages];
  const groups = messagesToGroups(messages);
  const tokenUsage = liveTokenUsage ?? data.token_usage;
  const contextUsage = liveContextUsage ?? data.context_usage;
  const canSend = sessionCanSend(data);
  const submitAction = composerSubmitAction({
    turnActive,
    compactActive,
    text: draft,
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Conversation className="min-h-0 flex-1">
        <ConversationContent className="mx-auto w-full max-w-[760px]">
          {data.has_more_before ||
          loadingOlderMessages ||
          olderMessagesError ? (
            <LoadOlderMessagesControl
              disabled={loadingOlderMessages || !data.oldest_message_id}
              error={olderMessagesError}
              loading={loadingOlderMessages}
              onLoad={() => void handleLoadOlderMessages()}
            />
          ) : null}
          {groups.map((group) => (
            <MessageGroupView
              key={group.key}
              group={group}
              compactCommand={
                group.id ? compactCommandInputs[group.id] : undefined
              }
            />
          ))}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>
      <div className="border-t bg-background/92 px-4 py-3 backdrop-blur md:px-6">
        <div className="mx-auto w-full max-w-[760px]">
          <QueuedInputStack items={queuedInputState.items} />
          {canSend ? (
            <PromptInput
              onSubmit={async (msg) => {
                const text = msg.text?.trim();
                if (!text) {
                  showComposerHint("Enter a message to send");
                  return;
                }
                setDraft("");
                await handleSend(text);
              }}
            >
              <PromptInputTextarea
                onChange={(event) => {
                  setDraft(event.currentTarget.value);
                  if (composerHint) setComposerHint(null);
                  if (status.kind === "error") setStatus({ kind: "idle" });
                }}
                placeholder="Ask juex anything..."
              />
              <PromptInputFooter className="flex-nowrap items-end gap-2">
                <TooltipProvider>
                  <PromptInputTools className="min-w-0 flex-1 flex-wrap gap-1.5">
                    {composerHint ? (
                      <ComposerFeedback tone="hint">
                        {composerHint}
                      </ComposerFeedback>
                    ) : null}
                    {status.kind === "error" ? (
                      <ComposerFeedback tone="error">
                        {status.detail ?? "Something went wrong"}
                      </ComposerFeedback>
                    ) : null}
                    <ContextUsageLabel
                      usage={contextUsage}
                      activeContext={activeContext}
                    />
                    <TokenUsageLabel usage={tokenUsage} />
                  </PromptInputTools>
                  <div className="flex shrink-0 items-center gap-1">
                    <ComposerSubmitButton
                      action={submitAction}
                      onCompacting={() =>
                        showComposerHint(COMPACTING_SUBMIT_HINT)
                      }
                      onEmpty={() => showComposerHint("Enter a message to send")}
                      onStop={() => void handleInterrupt()}
                    />
                  </div>
                </TooltipProvider>
              </PromptInputFooter>
            </PromptInput>
          ) : (
            <ReadOnlySessionBar data={data} />
          )}
        </div>
      </div>
    </div>
  );

  async function handleLoadOlderMessages() {
    if (!data?.oldest_message_id || loadingOlderMessages) return;
    const requestId = id;
    const before = data.oldest_message_id;
    setLoadingOlderMessages(true);
    setOlderMessagesError(null);
    try {
      const page = await getSession(requestId, { before });
      if (!isLatestRoute(latestRouteRef.current, requestId)) {
        return;
      }
      setData((prev) => mergeOlderSessionPage(prev, page));
    } catch (error) {
      if (isLatestRoute(latestRouteRef.current, requestId)) {
        setOlderMessagesError(
          error instanceof Error ? error.message : String(error),
        );
      }
    } finally {
      if (isLatestRoute(latestRouteRef.current, requestId)) {
        setLoadingOlderMessages(false);
      }
    }
  }

  function setTurnActiveControllerState(next: boolean) {
    turnActiveRef.current = next;
    setTurnActive(next);
  }

  function setCompactActiveControllerState(next: boolean) {
    setCompactActive(next);
  }

  function appendPendingCompact(commandInput?: string) {
    setLiveMessages((prev) => {
      let next = prev;
      if (
        commandInput &&
        !next.some((m) => m.id === LOCAL_COMPACT_COMMAND_ID)
      ) {
        next = [
          ...next,
          {
            id: LOCAL_COMPACT_COMMAND_ID,
            role: "user",
            kind: "slash_command",
            blocks: [{ type: "text", text: commandInput }],
          },
        ];
      }
      if (next.some((m) => m.id === LOCAL_COMPACT_PENDING_ID)) return next;
      return [
        ...next,
        {
          id: LOCAL_COMPACT_PENDING_ID,
          role: "user",
          kind: LOCAL_COMPACT_PENDING_KIND,
          pending: true,
          blocks: [{ type: "text", text: PENDING_COMPACT_LABEL }],
        },
      ];
    });
  }

  function clearLocalCompactMessages() {
    setLiveMessages((prev) => prev.filter((m) => !isLocalCompactMessage(m)));
  }

  function consumeQueuedInput(input: string | undefined, kind: string | undefined) {
    if (!input) return;
    const current = queuedInputStateRef.current;
    const index = current.items.findIndex(
      (item) => item.input === input && item.kind === kind,
    );
    if (index < 0) return;
    setQueuedInputControllerState({
      ...current,
      items: [
        ...current.items.slice(0, index),
        ...current.items.slice(index + 1),
      ],
    });
  }

  function appendLiveTurn(
    turnID: string | undefined,
    input: string | undefined,
    kind: string | undefined,
    source: "event" | "optimistic",
  ) {
    if (!turnID || !input) return;
    setLiveMessages((prev) => {
      if (prev.some((m) => m.turn_id === turnID)) return prev;
      const existingTurnID = findPendingTurnForInput(prev, input);
      if (existingTurnID) {
        if (source === "optimistic") return prev;
        return prev.map((m) =>
          m.turn_id === existingTurnID ? { ...m, turn_id: turnID } : m,
        );
      }
      return [
        ...prev,
        {
          role: "user",
          turn_id: turnID,
          kind,
          blocks: [{ type: "text", text: input }],
        },
        {
          role: "assistant",
          turn_id: turnID,
          pending: true,
          blocks: [],
        },
      ];
    });
  }

  function enqueueQueuedInput(
    input: string | undefined,
    kind: string | undefined,
    pendingCount: number,
  ) {
    setQueuedInputControllerState(
      enqueueQueuedInputState(
        queuedInputStateRef.current,
        input,
        kind,
        pendingCount,
      ),
    );
  }

  function drainQueuedInputs(count: number, turnID: string | undefined) {
    const result = drainQueuedInputState(queuedInputStateRef.current, count);
    setQueuedInputControllerState(result.state);
    appendDrainedInputs(result.drained, turnID);
  }

  function dropQueuedInputs(count: number) {
    setQueuedInputControllerState(
      dropQueuedInputState(queuedInputStateRef.current, count),
    );
  }

  function setQueuedInputControllerState(next: QueuedInputState) {
    queuedInputStateRef.current = next;
    setQueuedInputState(next);
  }

  function clearQueuedInputs() {
    if (queuedInputStateRef.current.items.length === 0) return;
    setQueuedInputControllerState(createQueuedInputState());
  }

  function showComposerHint(message: string) {
    setComposerHint(message);
    if (composerHintTimerRef.current) {
      window.clearTimeout(composerHintTimerRef.current);
    }
    composerHintTimerRef.current = window.setTimeout(
      () => setComposerHint(null),
      1800,
    );
  }

  function rememberCompactCommand(messageID: string | undefined, input: string) {
    if (!messageID) return;
    setCompactCommandInputs((prev) => ({ ...prev, [messageID]: input }));
  }

  function appendDrainedInputs(items: QueuedInput[], turnID: string | undefined) {
    if (!items.length) return;
    const additions: ChatMessage[] = items.map((item) => ({
      role: "user",
      turn_id: turnID,
      kind: item.kind || "pending_input",
      blocks: [{ type: "text", text: item.input }],
    }));
    setLiveMessages((prev) => {
      if (!turnID) return [...prev, ...additions];
      const insertAt = prev.findIndex(
        (m) => m.turn_id === turnID && m.role === "assistant" && m.pending,
      );
      if (insertAt < 0) return [...prev, ...additions];
      return [
        ...prev.slice(0, insertAt),
        ...additions,
        ...prev.slice(insertAt),
      ];
    });
  }

  function appendCommandResult(input: string, output: string) {
    setLiveMessages((prev) => [
      ...prev,
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
    ]);
  }

  function markDoneSoon() {
    setStatus({ kind: "done" });
    if (doneTimerRef.current) window.clearTimeout(doneTimerRef.current);
    doneTimerRef.current = window.setTimeout(
      () => setStatus({ kind: "idle" }),
      1500,
    );
  }

  function applyAssistantResponse(e: { turn_id?: string; payload?: unknown }) {
    if (!e.turn_id) return;
    const blocks = assistantBlocksFromEventPayload(e.payload);
    const model = eventString(e, "model");
    setLiveMessages((prev) => {
      let found = false;
      const next = prev.map((m) => {
        if (m.turn_id === e.turn_id && m.role === "assistant" && m.pending) {
          found = true;
          return { ...m, pending: false, blocks, model };
        }
        return m;
      });
      if (found) return next;
      return [
        ...next,
        { role: "assistant", turn_id: e.turn_id, pending: false, blocks, model },
      ];
    });
  }

  function applyTokenUsage(e: { payload?: unknown }) {
    const usage = eventTokenUsage(e);
    if (usage) setLiveTokenUsage(usage);
  }

  function applyContextUsage(e: { payload?: unknown }) {
    const usage = eventContextUsage(e);
    if (usage) setLiveContextUsage(usage);
  }

  function appendToolResult(e: { turn_id?: string; payload?: unknown }, isError: boolean) {
    if (!e.turn_id) return;
    const toolUseID = eventString(e, "tool_use_id");
    const content =
      eventString(e, isError ? "error" : "preview") ??
      eventString(e, "error") ??
      eventString(e, "preview") ??
      "";
    if (!toolUseID && !content) return;
    const block: NonNullable<ChatMessage["blocks"]>[number] = {
      type: "tool_result",
      tool_use_id: toolUseID,
      content,
      is_error: isError,
    };
    setLiveMessages((prev) => {
      const last = prev[prev.length - 1];
      if (
        last?.role === "user" &&
        last.turn_id === e.turn_id &&
        last.blocks?.every((b) => b.type === "tool_result")
      ) {
        return [
          ...prev.slice(0, -1),
          { ...last, blocks: [...(last.blocks ?? []), block] },
        ];
      }
      return [
        ...prev,
        {
          role: "user",
          turn_id: e.turn_id,
          blocks: [block],
        },
      ];
    });
  }
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
          <LoaderCircleIcon className="size-3.5 animate-spin" aria-hidden="true" />
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

function findPendingTurnForInput(
  messages: ChatMessage[],
  input: string,
): string | undefined {
  for (const message of messages) {
    if (message.role !== "user" || messageText(message) !== input) continue;
    const turnID = message.turn_id;
    if (
      turnID &&
      messages.some(
        (m) => m.role === "assistant" && m.turn_id === turnID && m.pending,
      )
    ) {
      return turnID;
    }
  }
  return undefined;
}

function messageText(message: ChatMessage): string | undefined {
  const block = message.blocks?.find((b) => b.type === "text");
  return block && "text" in block && typeof block.text === "string"
    ? block.text
    : undefined;
}

function eventErrorDetail(e: { payload?: unknown }): string | undefined {
  if (
    e.payload &&
    typeof e.payload === "object" &&
    "error" in e.payload &&
    typeof (e.payload as Record<string, unknown>).error === "string"
  ) {
    return (e.payload as Record<string, string>).error;
  }
  return undefined;
}

function eventInput(e: { payload?: unknown }): string | undefined {
  return eventString(e, "input");
}

function eventKind(e: { payload?: unknown }): string | undefined {
  return eventString(e, "kind") || undefined;
}

function eventPendingCount(e: { payload?: unknown }): number {
  if (!e.payload || typeof e.payload !== "object") return 0;
  const payload = e.payload as Record<string, unknown>;
  return eventNumber(payload.pending_count) ?? eventNumber(payload.count) ?? 0;
}

function eventDeltaCount(e: { payload?: unknown }): number {
  if (!e.payload || typeof e.payload !== "object") return 0;
  // Drained/dropped events use count for the affected item count; pending_count
  // is the remaining queue size and is normally zero.
  return eventNumber((e.payload as Record<string, unknown>).count) ?? 0;
}

function eventTokenUsage(e: { payload?: unknown }): TokenUsage | undefined {
  if (!e.payload || typeof e.payload !== "object") return undefined;
  const payload = e.payload as Record<string, unknown>;
  const raw = payload.token_usage;
  if (!raw || typeof raw !== "object") return undefined;
  const usage = raw as Record<string, unknown>;
  const input = eventNumber(usage.input_tokens);
  const output = eventNumber(usage.output_tokens);
  if (input === undefined || output === undefined) return undefined;
  return { input_tokens: input, output_tokens: output };
}

function eventContextUsage(e: { payload?: unknown }): ContextUsage | undefined {
  if (!e.payload || typeof e.payload !== "object") return undefined;
  const payload = e.payload as Record<string, unknown>;
  const raw = payload.context_usage;
  if (!raw || typeof raw !== "object") return undefined;
  const usage = raw as Record<string, unknown>;
  const input = eventNumber(usage.input_tokens);
  const output = eventNumber(usage.output_tokens);
  const total = eventNumber(usage.total_tokens);
  if (input === undefined || output === undefined || total === undefined) {
    return undefined;
  }
  const contextWindow = eventNumber(usage.context_window);
  const model = typeof usage.model === "string" ? usage.model : undefined;
  const breakdown = Array.isArray(usage.breakdown)
    ? usage.breakdown.flatMap((part) => {
        if (!part || typeof part !== "object") return [];
        const record = part as Record<string, unknown>;
        const key = typeof record.key === "string" ? record.key : "";
        const label = typeof record.label === "string" ? record.label : "";
        const tokens = eventNumber(record.tokens);
        if (!key || !label || tokens === undefined) return [];
        return [{ key, label, tokens }];
      })
    : undefined;
  return {
    model,
    context_window: contextWindow,
    input_tokens: input,
    output_tokens: output,
    total_tokens: total,
    breakdown,
  };
}

function eventNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}

function eventNumberFromPayload(e: { payload?: unknown }, key: string): number | undefined {
  if (!e.payload || typeof e.payload !== "object") return undefined;
  return eventNumber((e.payload as Record<string, unknown>)[key]);
}

function eventString(e: { payload?: unknown }, key: string): string | undefined {
  if (
    e.payload &&
    typeof e.payload === "object" &&
    key in e.payload &&
    typeof (e.payload as Record<string, unknown>)[key] === "string"
  ) {
    return (e.payload as Record<string, string>)[key];
  }
  return undefined;
}

function eventRecord(
  e: { payload?: unknown },
  key: string,
): Record<string, unknown> | undefined {
  if (!e.payload || typeof e.payload !== "object") return undefined;
  const value = (e.payload as Record<string, unknown>)[key];
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return undefined;
  }
  return value as Record<string, unknown>;
}

function TokenUsageLabel({ usage }: { usage: TokenUsage }) {
  const input = usage?.input_tokens ?? 0;
  const output = usage?.output_tokens ?? 0;
  const total = input + output;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex shrink-0 items-center rounded-full border border-transparent bg-muted px-2.5 py-1 font-mono text-[11px] text-muted-foreground">
          tokens {formatTokenCount(total)}
        </span>
      </TooltipTrigger>
      <TooltipContent>
        {formatTokenCount(input)} in / {formatTokenCount(output)} out
      </TooltipContent>
    </Tooltip>
  );
}

function ComposerFeedback({
  children,
  tone,
}: {
  children: string;
  tone: "hint" | "error";
}) {
  return (
    <span
      className={cn(
        "min-w-0 truncate font-mono text-[11px]",
        tone === "error" ? "text-juex-error" : "text-muted-foreground",
      )}
      title={children}
    >
      {children}
    </span>
  );
}

function ComposerSubmitButton({
  action,
  onCompacting,
  onEmpty,
  onStop,
}: {
  action: ComposerSubmitAction;
  onCompacting: () => void;
  onEmpty: () => void;
  onStop: () => void;
}) {
  const isEmpty = action === "empty";
  const isCompacting = action === "compacting";
  const isStop = action === "stop";
  const tooltip =
    action === "empty"
      ? "Enter a message to send"
      : action === "compacting"
        ? COMPACTING_SUBMIT_HINT
      : action === "stop"
        ? "Stop current turn"
        : action === "queue"
          ? "Queue message"
          : "Send message";
  const ariaLabel =
    action === "empty"
      ? "Enter a message before sending"
      : action === "compacting"
        ? COMPACTING_SUBMIT_HINT
      : action === "stop"
        ? "Stop current turn"
        : action === "queue"
          ? "Queue message"
          : "Send message";

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <PromptInputSubmit
          aria-disabled={isEmpty || isCompacting}
          aria-label={ariaLabel}
          className={cn(
            (isEmpty || isCompacting) && "cursor-not-allowed opacity-50",
          )}
          onClick={(event) => {
            if (isEmpty) {
              event.preventDefault();
              onEmpty();
              return;
            }
            if (isCompacting) {
              event.preventDefault();
              onCompacting();
              return;
            }
            if (isStop) {
              event.preventDefault();
              onStop();
            }
          }}
          type={isEmpty || isCompacting || isStop ? "button" : "submit"}
          variant={isStop ? "outline" : "default"}
        >
          {isStop ? (
            <SquareIcon className="size-4" aria-hidden="true" />
          ) : (
            <SendHorizontalIcon className="size-4" aria-hidden="true" />
          )}
        </PromptInputSubmit>
      </TooltipTrigger>
      <TooltipContent>{tooltip}</TooltipContent>
    </Tooltip>
  );
}

function ReadOnlySessionBar({ data }: { data: SessionShowResponse }) {
  return (
    <div className="flex min-h-[52px] flex-wrap items-center gap-3 rounded-md border bg-muted/50 px-3 py-2 text-sm">
      <div className="min-w-0 text-muted-foreground">
        {sessionReadOnlyMessage(data)}
      </div>
    </div>
  );
}

function ContextUsageLabel({
  usage,
  activeContext,
}: {
  usage?: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex shrink-0 items-center rounded-full border border-transparent bg-muted px-2.5 py-1 font-mono text-[11px] text-muted-foreground">
          context {usage ? formatTokenCount(usage.total_tokens) : "-"}
        </span>
      </TooltipTrigger>
      <TooltipContent className="block max-w-sm space-y-1.5 px-3 py-2 font-mono text-xs">
        {usage ? (
          <ContextUsageTooltip usage={usage} activeContext={activeContext} />
        ) : (
          <>
            <div>No context usage yet</div>
            <ActiveContextDebugLine snapshot={activeContext} />
          </>
        )}
      </TooltipContent>
    </Tooltip>
  );
}

function ContextUsageTooltip({
  usage,
  activeContext,
}: {
  usage: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
}) {
  const windowTokens = usage.context_window ?? 0;
  const percent =
    windowTokens > 0
      ? Math.round((usage.total_tokens / windowTokens) * 1000) / 10
      : 0;
  return (
    <>
      <div>
        {(usage.model && usage.model.trim()) || "unknown"}{" "}
        {formatTokenCount(usage.total_tokens)}/{formatTokenCount(windowTokens)} tokens (
        {formatPercent(percent)})
      </div>
      {usage.cached_input_tokens ? (
        <div>
          cached input: {formatTokenCount(usage.cached_input_tokens)} tokens (
          {formatPercent((usage.cached_input_tokens / Math.max(usage.input_tokens, 1)) * 100)})
        </div>
      ) : null}
      <div className="text-muted-foreground">estimated breakdown</div>
      <div className="space-y-0.5">
        {(usage.breakdown ?? []).map((part) => (
          <div key={part.key}>
            - {part.label}: {formatTokenCount(part.tokens)} tokens
            {windowTokens > 0
              ? ` (${formatPercent((part.tokens / windowTokens) * 100)})`
              : ""}
          </div>
        ))}
      </div>
      <ActiveContextDebugLine snapshot={activeContext} />
    </>
  );
}

function ActiveContextDebugLine({
  snapshot,
}: {
  snapshot?: ActiveContextSnapshot | null;
}) {
  if (!snapshot) return null;
  const count = snapshot?.messages?.length ?? 0;
  const tokens = snapshot?.estimated_tokens ?? 0;
  return (
    <div className="text-muted-foreground">
      active provider context {count} messages,{" "}
      {formatTokenCount(tokens)} estimated tokens
    </div>
  );
}

function formatTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return value === 0 ? "0" : "-";
  if (value >= 1_000_000) return `${trimFixed(value / 1_000_000)}m`;
  if (value >= 1_000) return `${trimFixed(value / 1_000)}k`;
  return Math.round(value).toString();
}

function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return "0%";
  if (value > 0 && value < 0.1) return "0.0%";
  return `${trimFixed(value)}%`;
}

function trimFixed(value: number): string {
  return value.toFixed(1).replace(/\.0$/, "");
}

function MessageGroupView({
  group,
  compactCommand,
}: {
  group: MessageGroup;
  compactCommand?: string;
}) {
  // Per-message model (stamped at generation time). Falls back to nothing
  // for older messages that pre-date the persistence change; the header
  // already shows the current session-level model in that case.
  const showModel = group.role === "assistant" && !!group.model;
  const isMCPEvent = group.role === "user" && group.kind === "mcp_event";
  const isCompact = group.kind === "compact";
  const isPendingCompact = group.kind === LOCAL_COMPACT_PENDING_KIND;
  const copyText = messageGroupCopyText(group);
  const canCopyMessage = !isPendingCompact && messageGroupCanCopy(group);

  if (isMCPEvent) {
    return <MCPEventGroup group={group} />;
  }

  if (isCompact) {
    const textUnit = group.units.find((unit) => unit.kind === "text");
    const text = textUnit?.kind === "text" ? textUnit.block.text : "";
    return (
      <>
        {compactCommand ? <SlashCommandMessage text={compactCommand} /> : null}
        <CompactMessage text={text} />
      </>
    );
  }

  if (isPendingCompact) {
    return <CompactMessage text={PENDING_COMPACT_LABEL} state="pending" />;
  }

  const isEmpty = group.units.length === 0;

  return (
    <Message from={group.role}>
      <div className="flex w-full flex-col gap-2">
        {showModel ? (
          <span className="font-mono text-[11px] text-muted-foreground">
            {group.model}
          </span>
        ) : null}
        {group.units.map((unit, i) => {
          if (unit.kind === "text") {
            if (isCompact) {
              return <CompactMessage key={i} text={unit.block.text} />;
            }
            return (
              <MessageContent key={i}>
                <MessageResponse>{unit.block.text}</MessageResponse>
              </MessageContent>
            );
          }
          if (unit.kind === "reasoning") {
            const text = unit.block.text ?? unit.block.content ?? "";
            if (unit.block.redacted) {
              return (
                <Reasoning key={i} isStreaming={false}>
                  <ReasoningTrigger>Thinking [redacted]</ReasoningTrigger>
                  <ReasoningContent>
                    [redacted by provider]
                  </ReasoningContent>
                </Reasoning>
              );
            }
            return (
              <Reasoning key={i} isStreaming={false}>
                <ReasoningTrigger />
                <ReasoningContent>{text}</ReasoningContent>
              </Reasoning>
            );
          }
          // unit.kind === "tool"
          const state = toolState(unit.use, unit.result);
          const toolName = unit.use?.tool_name ?? "tool";
          return (
            <Tool
              key={i}
              defaultOpen={state === "output-error" || state === "input-available"}
            >
              <ToolHeader
                type={`tool-${toolName}`}
                state={state}
                timeoutSeconds={unit.use?.timeout_seconds}
              />
              <ToolContent>
                {unit.use ? <ToolInput input={unit.use.input} /> : null}
                {unit.result ? (
                  <ToolOutput
                    output={unit.result.is_error ? null : unit.result.content}
                    errorText={unit.result.is_error ? unit.result.content : undefined}
                  />
                ) : null}
              </ToolContent>
            </Tool>
          );
        })}
        {group.pending && isEmpty ? (
          <div className="animate-pulse text-sm text-muted-foreground">...</div>
        ) : null}
        {canCopyMessage ? (
          <MessageCopyAction
            text={copyText}
            align={group.role === "user" ? "end" : "start"}
          />
        ) : null}
      </div>
    </Message>
  );
}

function SlashCommandMessage({ text }: { text: string }) {
  return (
    <Message from="user">
      <div className="flex w-full flex-col gap-2">
        <MessageContent>
          <MessageResponse>{text}</MessageResponse>
        </MessageContent>
        <MessageCopyAction text={text} align="end" />
      </div>
    </Message>
  );
}

function MCPEventGroup({ group }: { group: MessageGroup }) {
  const isEmpty = group.units.length === 0;
  return (
    <div className="flex w-full justify-center px-2 py-0.5">
      <div className="flex w-full max-w-[min(34rem,100%)] flex-col gap-2">
        {group.units.map((unit, i) => {
          if (unit.kind !== "text") return null;
          return <MCPEventMessage key={i} text={unit.block.text} />;
        })}
        {group.pending && isEmpty ? (
          <div className="text-center text-sm text-muted-foreground">...</div>
        ) : null}
      </div>
    </div>
  );
}

function CompactMessage({
  text,
  state = "complete",
}: {
  text: string;
  state?: "complete" | "pending";
}) {
  if (state === "pending") {
    return (
      <div className="flex w-full items-center gap-3 px-2 py-3">
        <Separator className="flex-1 opacity-60" />
        <span className="rounded-full border border-border/70 bg-background/70 px-3 py-1 font-mono text-[11px] text-muted-foreground/70 shadow-[var(--shadow-xs)]">
          {text}
        </span>
        <Separator className="flex-1 opacity-60" />
      </div>
    );
  }
  const summary = compactSummaryText(text);
  return (
    <div className="flex w-full items-center gap-3 px-2 py-3">
      <Separator className="flex-1" />
      <CopyTextButton
        text={summary}
        className="h-7 rounded-full border border-border bg-background px-3 font-mono text-[11px] text-muted-foreground shadow-[var(--shadow-xs)] hover:text-foreground"
        copiedTooltip={COMPACT_COPIED_TOOLTIP}
        idleTooltip="Copy compacted context"
        label="Copy compacted context"
        size="sm"
        tooltipMode="copied-only"
      >
        Context compacted
      </CopyTextButton>
      <Separator className="flex-1" />
    </div>
  );
}

function MessageCopyAction({
  text,
  align,
}: {
  text: string;
  align: "start" | "end";
}) {
  return (
    <MessageActions
      className={cn(
        "opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100",
        align === "end" ? "justify-end pr-1" : "justify-start pl-1",
      )}
    >
      <CopyTextButton
        text={text}
        className="size-6 text-muted-foreground hover:text-foreground"
        copiedTooltip="Copied to clipboard"
        idleTooltip="Copy message"
        label="Copy message"
        size="icon-xs"
        tooltipMode="none"
      />
    </MessageActions>
  );
}

function CopyTextButton({
  text,
  className,
  idleTooltip,
  copiedTooltip,
  label,
  size = "icon-sm",
  tooltipMode,
  children,
}: {
  text: string;
  className?: string;
  idleTooltip: string;
  copiedTooltip: string;
  label?: string;
  size?:
    | "default"
    | "xs"
    | "sm"
    | "lg"
    | "icon"
    | "icon-xs"
    | "icon-sm"
    | "icon-lg";
  tooltipMode?: CopyTooltipMode;
  children?: ReactNode;
}) {
  const [copySignal, setCopySignal] = useState(0);
  const copied = copySignal > 0;
  const effectiveTooltipMode =
    tooltipMode ??
    copyButtonDefaultTooltipMode({ hasVisibleLabel: Boolean(children) });
  const tooltip = copyButtonTooltip({
    copied,
    mode: effectiveTooltipMode,
    idleTooltip,
    copiedTooltip,
  });
  const tooltipOpen = effectiveTooltipMode === "copied-only" ? copied : undefined;

  useEffect(() => {
    if (!copySignal) return;
    const reset = window.setTimeout(() => setCopySignal(0), 1800);
    return () => window.clearTimeout(reset);
  }, [copySignal]);

  async function copyText() {
    if (!text) return;
    try {
      await writeClipboardText(text);
      setCopySignal((current) => current + 1);
    } catch (error) {
      console.error("copy text failed", error);
    }
  }

  return (
    <MessageAction
      className={className}
      label={label ?? idleTooltip}
      onClick={() => void copyText()}
      size={size}
      tooltip={tooltip}
      tooltipOpen={tooltipOpen}
      variant="ghost"
    >
      {children ?? (
        <>
          {copied ? (
            <CheckIcon className="size-3.5" aria-hidden="true" />
          ) : (
            <CopyIcon className="size-3.5" aria-hidden="true" />
          )}
        </>
      )}
    </MessageAction>
  );
}

function MCPEventMessage({ text }: { text: string }) {
  const [expanded, setExpanded] = useState(false);
  const event = formatMCPEventForDisplay(text);
  const toggleLabel = expanded ? "Collapse MCP event" : "Expand MCP event";

  return (
    <div
      className="w-full overflow-hidden rounded-lg border border-juex-gold-300 bg-juex-gold-50/95 text-juex-gold-900 shadow-[var(--shadow-xs)] dark:border-juex-gold-400/25 dark:bg-juex-gold-400/10 dark:text-juex-gold-400"
      data-mcp-event-message
    >
      <div className="flex min-w-0 items-center gap-2 px-3 py-2 text-xs">
        <RadioIcon className="size-3.5 shrink-0" aria-hidden="true" />
        <span
          className="min-w-0 max-w-[48%] shrink-0 truncate font-mono font-semibold sm:max-w-[18rem]"
          data-mcp-event-label
        >
          {event.label}
        </span>
        <span
          className="size-1 shrink-0 rounded-full bg-current opacity-45"
          aria-hidden="true"
        />
        <span
          className="min-w-0 flex-1 truncate text-[12px] text-juex-ink-600 dark:text-juex-cream-100/75"
          data-mcp-event-preview
        >
          {event.preview}
        </span>
        <span className="shrink-0" data-mcp-event-copy>
          <CopyTextButton
            text={event.copyText}
            className="size-7 border border-transparent text-current opacity-80 hover:border-juex-gold-300 hover:bg-juex-gold-100 hover:text-current hover:opacity-100 focus-visible:ring-ring dark:hover:border-juex-gold-400/30 dark:hover:bg-juex-gold-400/10"
            copiedTooltip="Copied to clipboard"
            idleTooltip="Copy event content"
            label="Copy event content"
            size="icon-sm"
          />
        </span>
        <button
          type="button"
          className="inline-flex size-7 shrink-0 items-center justify-center rounded-md border border-transparent text-current opacity-80 transition hover:border-juex-gold-300 hover:bg-juex-gold-100 hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:hover:border-juex-gold-400/30 dark:hover:bg-juex-gold-400/10"
          aria-expanded={expanded}
          aria-label={toggleLabel}
          title={toggleLabel}
          data-mcp-event-toggle
          onClick={() => setExpanded((value) => !value)}
        >
          <ChevronDownIcon
            className={cn("size-3.5 transition-transform", expanded && "rotate-180")}
            aria-hidden="true"
          />
        </button>
      </div>
      {expanded ? (
        <div
          className="border-t border-juex-gold-300/75 px-3 py-3 text-[13px] leading-6 text-juex-ink-900 dark:border-juex-gold-400/20 dark:text-juex-cream-50"
          data-mcp-event-body
        >
          <MessageResponse className="break-words">{event.content}</MessageResponse>
        </div>
      ) : null}
    </div>
  );
}
