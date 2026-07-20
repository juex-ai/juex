import {
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { useStickToBottomContext } from "use-stick-to-bottom";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
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
  PromptInput,
  PromptInputButton,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
  usePromptInputAttachments,
} from "@/components/ai-elements/prompt-input";
import { useShellTitle } from "@/components/AppShell";
import { AgentRuntimeStateBar } from "@/components/fleet/AgentRuntimeStateBar";
import {
  useAgentSessionStatus,
  useFleetAgent,
} from "@/components/fleet/FleetAgentContext";
import { AssistantMarkdown } from "@/components/AssistantMarkdown";
import { ImageBlock } from "@/components/ImageBlock";
import { LoadingState } from "@/components/LoadingState";
import {
  messagesToGroups,
  toolState,
  type MessageGroup,
  type ToolDisplayUnit,
} from "@/lib/display-units";
import {
  assistantWorkItems,
  assistantWorkTitle,
  transcriptItemModelLabels,
  type AssistantWorkItem,
} from "@/lib/assistant-work-groups";
import {
  QUEUE_FULL_SUBMIT_HINT,
  composerErrorMessage,
  composerSubmitAction,
  settleSubmittedComposerText,
  type ComposerSubmitAction,
} from "@/lib/composer-submit";
import {
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
import {
  aggregateToolProcessStatus,
  formatToolBatchTitle,
  formatToolProcessResult,
  thinkingProcessDisplay,
  thinkingProcessVisibleText,
  toolDisplayName,
  toolProcessStatus,
  toolProcessStatusLabel,
  toolTimeoutLabel,
  type ToolProcessStatus,
} from "@/lib/tool-display";
import { sessionPreviewTitle } from "@/lib/session-title";
import {
  formatMCPEventForDisplay,
  formatObservationEventForDisplay,
} from "@/lib/mcp-events";
import { formatModelFallbackNotice } from "@/lib/model-fallback";
import {
  externalEventBodyClassName,
  externalEventCopyClassName,
  externalEventRowClassName,
  processDisclosureBodyClassName,
  processDisclosureChevronClassName,
  processDisclosureClassName,
  processDisclosureSummaryClassName,
  thinkingDisclosureBodyClassName,
  thinkingDisclosureSummaryClassName,
} from "@/lib/message-rendering";
import { cn } from "@/lib/utils";
import {
  formatRuntimeTimestamp,
  notesCheckboxProgress,
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeGoalContinuationLabel,
  runtimeSessionStateBadgeLabel,
  runtimeSessionStateIsActive,
  runtimeTokenUsageDetailLabel,
} from "@/lib/runtime-display";
import { QueuedInputStack } from "@/components/QueuedInputStack";
import { Separator } from "@/components/ui/separator";
import {
  createSessionReadState,
  type SessionInitialCommandState,
  type SessionReadState,
} from "@/lib/session-read-state";
import {
  createSessionReadController,
  type SessionReadController,
} from "@/lib/session-read-controller";
import {
  getSession,
  getSessionContext,
  getSessionStatus,
  interrupt,
  startTurn,
  subscribeEvents,
  subscribeSessionStatus,
  uploadSessionAttachment,
} from "@/api";
import { sessionCanSend, sessionReadOnlyMessage } from "@/lib/session-access";
import { agentPathFromLocation } from "@/lib/fleet-routes";
import {
  CheckIcon,
  CircleAlertIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  CircleGaugeIcon,
  CopyIcon,
  ImagePlusIcon,
  LoaderCircleIcon,
  RadioIcon,
  SendHorizontalIcon,
  SquareIcon,
  XIcon,
} from "lucide-react";
import type {
  ActiveContextSnapshot,
  ContextUsage,
  GoalStatusSnapshot,
  MediaRef,
  Message as ChatMessage,
  NotesSnapshot,
  SessionShowResponse,
  TokenUsage,
} from "@/types";

type InitialCommandState = SessionInitialCommandState;

const COMPOSER_STATUS_CONTROL_CLASS =
  "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-sm border border-border/70 bg-background px-2 font-mono text-[11px] text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35 focus-visible:ring-offset-2 focus-visible:ring-offset-background";

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
  const [draft, setDraft] = useState("");
  const [attachmentCount, setAttachmentCount] = useState(0);
  const [composerOverlayNode, setComposerOverlayNode] =
    useState<HTMLDivElement | null>(null);
  const [composerOverlayHeight, setComposerOverlayHeight] = useState(0);
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

  useLayoutEffect(() => {
    if (!composerOverlayNode) {
      setComposerOverlayHeight(0);
      return;
    }
    const measure = () => {
      setComposerOverlayHeight(
        Math.ceil(composerOverlayNode.getBoundingClientRect().height),
      );
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(composerOverlayNode);
    return () => observer.disconnect();
  }, [composerOverlayNode]);

  useEffect(() => {
    const state = location.state as InitialCommandState;
    const activeTurnID = state?.activeTurnID;
    controller.setRoute(id);
    controller.resetForRoute({ activeTurnID });
    setDraft("");
    // location.state is read only on session entry; clearing it later must not
    // reset live projection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [controller, id]);

  useEffect(() => {
    if (!id) return;
    void controller.refresh(id, {
      preserveLiveMessages: true,
      recordLoadFailure: true,
    });
  }, [controller, id]);

  const canSubscribeSessionStatus = data ? sessionCanSend(data) : false;

  useEffect(() => {
    if (
      !id ||
      !agent?.id ||
      !statusStore ||
      !agentRuntimeHealthy ||
      !canSubscribeSessionStatus
    ) {
      return;
    }
    let disposed = false;
    let unsubscribe = () => {};
    void getSessionStatus(id)
      .then((snapshot) => {
        if (disposed) return;
        statusStore.setStatus(agent.id, snapshot);
        unsubscribe = subscribeSessionStatus(id, {
          since: snapshot.cursor,
          onStatus: (next) => statusStore.setStatus(agent.id, next),
          onError: (event) => {
            statusStore.clearStatus(agent.id, id);
            console.error("session status stream failed", event);
          },
        });
      })
      .catch((error) => {
        statusStore.clearStatus(agent.id, id);
        console.error("getSessionStatus failed", error);
      });
    return () => {
      disposed = true;
      unsubscribe();
      statusStore.clearStatus(agent.id, id);
    };
  }, [
    agent?.id,
    agentRuntimeHealthy,
    canSubscribeSessionStatus,
    id,
    statusStore,
  ]);

  useEffect(() => {
    if (!data) return;
    const state = location.state as InitialCommandState;
    if (!state?.command || !state.commandInput) return;
    controller.projectInitialCommandOnce(id, state.commandInput, state.command);
  }, [controller, data, id, location.state]);

  // SSE subscription.
  useEffect(() => {
    if (!id || !data || !sessionCanSend(data) || !agentRuntimeHealthy) return;
    return controller.subscribeLiveEvents(id);
    // Keep this tied to send-access fields so projection-only data changes do
    // not reopen the EventSource during active turns.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentRuntimeHealthy, controller, data?.kind, data?.active, id]);

  async function handleSend(
    prompt: string,
    attachments: MediaRef[] = [],
  ): Promise<boolean> {
    return controller.submitPrompt(id, prompt, attachments);
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

  const messages = useMemo<ChatMessage[]>(
    () => [...(data?.messages ?? []), ...projection.messages],
    [data?.messages, projection.messages],
  );
  const groups = useMemo(() => messagesToGroups(messages), [messages]);
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
  const composerClearance =
    canSend && composerOverlayHeight > 0 ? composerOverlayHeight + 12 : 0;
  const submitAction = composerSubmitAction({
    status: runtimeStatus,
    text: draft,
    attachmentCount,
  });
  const composerError = composerErrorMessage({
    status: runtimeStatus,
    localError: submitError ?? undefined,
  });

  return (
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
      <Conversation className="min-h-0 flex-1">
        <ConversationClearanceFollower clearance={composerClearance} />
        <ConversationContent
          className="mx-auto w-full max-w-[760px]"
          style={{ paddingBottom: composerClearance || undefined }}
        >
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
          {transcriptItems.map((item, index) =>
            item.kind === "assistant_work" ? (
              <AssistantWorkGroupView
                key={item.key}
                work={item}
                modelLabel={modelLabels[index]}
              />
            ) : (
              <MessageGroupView
                key={item.key}
                group={item.group}
                modelLabel={modelLabels[index]}
                compactCommand={
                  item.group.id
                    ? projection.compactCommandInputs[item.group.id]
                    : undefined
                }
              />
            ),
          )}
        </ConversationContent>
        <ConversationScrollButton
          className="z-30"
          style={{ bottom: composerClearance ? composerClearance + 16 : 16 }}
        />
      </Conversation>
      {canSend ? (
        <div
          className="pointer-events-none absolute inset-0 z-20 flex items-end"
          data-testid="session-composer-overlay"
        >
          <div
            className="flex max-h-full w-full flex-col overflow-hidden bg-linear-to-b from-transparent via-background/90 to-background px-3 pt-[clamp(1.5rem,10dvh,4rem)] md:px-6"
          >
            <div
              className="pointer-events-none mx-auto flex min-h-0 w-full max-w-[760px] flex-col pb-[max(0.75rem,env(safe-area-inset-bottom))] md:pb-[max(1.25rem,env(safe-area-inset-bottom))]"
              data-testid="session-composer-obstruction"
              ref={setComposerOverlayNode}
            >
              <div
                className="pointer-events-auto flex min-h-0 flex-col overflow-hidden"
                data-testid="session-composer-stack"
              >
                <QueuedInputStack items={projection.queuedInput.items} />
                <PromptInput
                  accept="image/*"
                  className="shrink-0"
                  maxFileSize={10 * 1024 * 1024}
                  maxFiles={8}
                  multiple
                  onError={(err) => showComposerHint(err.message)}
                  onSubmit={async (msg) => {
                    if (submitAction === "loading") {
                      throw new Error("Loading session status");
                    }
                    if (submitAction === "queue-full") {
                      throw new Error(QUEUE_FULL_SUBMIT_HINT);
                    }
                    const submittedText = msg.text ?? "";
                    const text = submittedText.trim();
                    const files = msg.files ?? [];
                    if (!text && files.length === 0) {
                      showComposerHint("Enter a message or attach an image");
                      return;
                    }
                    const attachments = await uploadPromptAttachments(id, files);
                    const sent = await handleSend(text ?? "", attachments);
                    if (!sent) {
                      throw new Error("start turn failed");
                    }
                    setDraft((current) =>
                      settleSubmittedComposerText(current, submittedText),
                    );
                  }}
                >
                  <ComposerAttachmentStrip onCountChange={setAttachmentCount} />
                  <PromptInputTextarea
                    className="max-h-[min(12rem,30dvh)]"
                    onChange={(event) => {
                      setDraft(event.currentTarget.value);
                      controller.projectPromptInput();
                    }}
                    placeholder="Ask juex anything..."
                  />
                  {composerHint || composerError ? (
                    <div className="border-t border-border/60 px-2.5 py-1.5">
                      {composerError ? (
                        <ComposerFeedback tone="error">
                          {composerError}
                        </ComposerFeedback>
                      ) : composerHint ? (
                        <ComposerFeedback tone="hint">
                          {composerHint}
                        </ComposerFeedback>
                      ) : null}
                    </div>
                  ) : null}
                  <PromptInputFooter className="flex-nowrap items-end gap-2">
                    <TooltipProvider>
                      <PromptInputTools className="min-w-0 flex-1 flex-wrap gap-2">
                        <div
                          className="flex shrink-0 items-center gap-1"
                          aria-label="Composer actions"
                          role="group"
                        >
                          <ComposerAttachmentButton />
                        </div>
                        <Separator
                          className="h-4"
                          orientation="vertical"
                          decorative
                        />
                        <div
                          className="flex min-w-0 items-center gap-1"
                          aria-label="Session status"
                          role="group"
                        >
                          {runtimeStatus ? (
                            <ContextUsageLabel
                              usage={runtimeStatus.context_usage}
                              activeContext={activeContext}
                              tokenUsage={runtimeStatus.token_usage}
                            />
                          ) : (
                            <ComposerStatusLoading />
                          )}
                          <SessionRuntimeStateBadges data={data} />
                        </div>
                      </PromptInputTools>
                      <div className="flex shrink-0 items-center gap-1">
                        <ComposerSubmitButton
                          action={submitAction}
                          onEmpty={() =>
                            showComposerHint("Enter a message or attach an image")
                          }
                          onQueueFull={() =>
                            showComposerHint(QUEUE_FULL_SUBMIT_HINT)
                          }
                          onStop={() => void handleInterrupt()}
                        />
                      </div>
                    </TooltipProvider>
                  </PromptInputFooter>
                </PromptInput>
              </div>
            </div>
          </div>
        </div>
      ) : (
        <div className="shrink-0 px-4 py-3 md:px-6">
          <div className="mx-auto w-full max-w-[760px]">
            <QueuedInputStack items={projection.queuedInput.items} />
            {!agentRuntimeHealthy ? (
              <AgentRuntimeStateBar />
            ) : (
              <ReadOnlySessionBar data={data} />
            )}
          </div>
        </div>
      )}
    </div>
  );

  async function handleLoadOlderMessages() {
    await controller.loadOlderMessages(id, data?.oldest_message_id);
  }

  function showComposerHint(message: string) {
    controller.showComposerHint(message);
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

function SessionRuntimeStateBadges({ data }: { data: SessionShowResponse }) {
  return (
    <SessionStateBadge
      label={runtimeSessionStateBadgeLabel(data.goal, data.notes)}
      tone={
        runtimeSessionStateIsActive(data.goal, data.notes)
          ? "active"
          : "muted"
      }
    >
      <SessionStateTooltip goal={data.goal} notes={data.notes} />
    </SessionStateBadge>
  );
}

function SessionStateBadge({
  children,
  label,
  tone,
}: {
  children: ReactNode;
  label: string;
  tone: "active" | "muted";
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          className={cn(
            COMPOSER_STATUS_CONTROL_CLASS,
            tone === "active"
              ? "border-primary/30 text-primary"
              : "text-muted-foreground",
          )}
          type="button"
          aria-label={`Open goal and notes: ${label}`}
        >
          {label}
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="block !w-[min(34rem,calc(100vw-2rem))] !max-w-[calc(100vw-2rem)] max-h-[24rem] overflow-auto text-left text-xs"
      >
        {children}
      </PopoverContent>
    </Popover>
  );
}

function GoalStateTooltip({ goal }: { goal?: GoalStatusSnapshot }) {
  if (!goal) {
    return (
      <RuntimeTooltipPanel title="Goal">
        <div className="text-muted-foreground">No goal state for this session.</div>
      </RuntimeTooltipPanel>
    );
  }
  return (
    <RuntimeTooltipPanel title="Goal">
      <RuntimeTooltipRow label="status" value={goal.status || "unknown"} />
      <RuntimeTooltipRow label="description" value={goal.description || "-"} />
      <RuntimeTooltipRow label="acceptance" value={goal.acceptance || "-"} />
      <RuntimeTooltipRow label="reason" value={goal.status_reason || "-"} />
      <RuntimeTooltipRow
        label="continuations"
        value={runtimeGoalContinuationLabel(goal)}
      />
      <RuntimeTooltipRow label="updated" value={formatRuntimeTimestamp(goal.updated_at)} />
    </RuntimeTooltipPanel>
  );
}

function SessionStateTooltip({
  goal,
  notes,
}: {
  goal?: GoalStatusSnapshot;
  notes?: NotesSnapshot;
}) {
  return (
    <div className="space-y-3">
      <GoalStateTooltip goal={goal} />
      <div className="border-t border-border/60 pt-3">
        <NotesStateTooltip notes={notes} />
      </div>
    </div>
  );
}

function NotesStateTooltip({ notes }: { notes?: NotesSnapshot }) {
  if (!notes?.content?.trim()) {
    return (
      <RuntimeTooltipPanel title="Notes">
        <div className="text-muted-foreground">No working notes for this session.</div>
      </RuntimeTooltipPanel>
    );
  }
  const progress = notesCheckboxProgress(notes);
  return (
    <RuntimeTooltipPanel title="Notes">
      <RuntimeTooltipRow label="updated" value={formatRuntimeTimestamp(notes.updated_at)} />
      {progress.total > 0 ? (
        <div className="space-y-1.5">
          <RuntimeTooltipRow
            label="progress"
            value={`${progress.completed}/${progress.total} complete`}
          />
          <div
            aria-label="Notes task progress"
            aria-valuemax={progress.total}
            aria-valuemin={0}
            aria-valuenow={progress.completed}
            className="h-1.5 w-full overflow-hidden rounded-sm bg-muted"
            role="progressbar"
          >
            <div
              className="h-full bg-primary transition-[width]"
              style={{ width: `${progress.percent}%` }}
            />
          </div>
        </div>
      ) : null}
      <div className="border-t border-border/60 pt-2">
        <MessageResponse className="break-words text-xs leading-relaxed [&_h1]:!my-2 [&_h1]:!text-base [&_h2]:!my-2 [&_h2]:!text-sm [&_h3]:!my-1.5 [&_h3]:!text-xs">
          {notes.content}
        </MessageResponse>
      </div>
    </RuntimeTooltipPanel>
  );
}

function RuntimeTooltipPanel({
  children,
  title,
}: {
  children: ReactNode;
  title: string;
}) {
  return (
    <div className="min-w-[18rem] max-w-xl space-y-2">
      <div className="font-mono text-[11px] font-semibold uppercase tracking-normal text-muted-foreground">
        {title}
      </div>
      <div className="space-y-1.5">{children}</div>
    </div>
  );
}

function RuntimeTooltipRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-2 sm:grid-cols-[6rem_minmax(0,1fr)]">
      <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words text-popover-foreground">{value}</span>
    </div>
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
    <div
      className={cn(
        "min-w-0 break-words font-mono text-[11px]",
        tone === "error" ? "text-juex-error" : "text-muted-foreground",
      )}
      role={tone === "error" ? "alert" : "status"}
      aria-live={tone === "error" ? "assertive" : "polite"}
    >
      {children}
    </div>
  );
}

type PromptAttachmentFile = {
  filename?: string;
  mediaType?: string;
  url: string;
};

async function uploadPromptAttachments(
  sessionID: string,
  files: PromptAttachmentFile[],
): Promise<MediaRef[]> {
  if (files.length === 0) return [];
  return Promise.all(
    files.map(async (file) =>
      uploadSessionAttachment(sessionID, await filePartToFile(file)),
    ),
  );
}

async function filePartToFile(part: PromptAttachmentFile): Promise<File> {
  const response = await fetch(part.url);
  if (!response.ok) {
    throw new Error("Unable to read attached image");
  }
  const blob = await response.blob();
  const type = part.mediaType || blob.type || "application/octet-stream";
  const name = part.filename || "image";
  return new File([blob], name, { type });
}

function ComposerAttachmentButton() {
  const attachments = usePromptInputAttachments();
  return (
    <PromptInputButton
      aria-label="Attach images"
      onClick={() => attachments.openFileDialog()}
      tooltip="Attach images"
    >
      <ImagePlusIcon className="size-4" aria-hidden="true" />
    </PromptInputButton>
  );
}

function ComposerAttachmentStrip({
  onCountChange,
}: {
  onCountChange: (count: number) => void;
}) {
  const attachments = usePromptInputAttachments();
  const files = attachments.files;
  useEffect(() => {
    onCountChange(files.length);
  }, [files.length, onCountChange]);

  if (files.length === 0) return null;
  return (
    <ul
      aria-label="Attached images"
      className="flex max-h-[min(10.5rem,24dvh)] w-full flex-wrap items-start justify-start gap-2 overflow-y-auto overscroll-contain px-2.5 pt-2"
    >
      {files.map((file) => (
        <li
          key={file.id}
          className="relative size-20 shrink-0 overflow-hidden rounded-md border border-border/70 bg-muted"
        >
          <img
            src={file.url}
            alt={file.filename ?? "attached image"}
            className="size-full object-cover"
          />
          <Button
            aria-label={`Remove ${file.filename ?? "attached image"}`}
            className="absolute right-1 top-1 size-6 rounded-full bg-foreground text-background shadow-[var(--shadow-xs)] hover:bg-foreground/80 hover:text-background"
            onClick={() => attachments.remove(file.id)}
            size="icon"
            type="button"
            variant="ghost"
          >
            <XIcon className="size-3.5" aria-hidden="true" />
          </Button>
        </li>
      ))}
    </ul>
  );
}

function ComposerSubmitButton({
  action,
  onEmpty,
  onQueueFull,
  onStop,
}: {
  action: ComposerSubmitAction;
  onEmpty: () => void;
  onQueueFull: () => void;
  onStop: () => void;
}) {
  const isLoading = action === "loading";
  const isEmpty = action === "empty";
  const isQueueFull = action === "queue-full";
  const isStop = action === "stop";
  const tooltip =
    action === "loading"
      ? "Loading session status"
      : action === "empty"
      ? "Enter a message or attach an image"
      : action === "queue-full"
        ? QUEUE_FULL_SUBMIT_HINT
      : action === "stop"
        ? "Stop current turn"
        : action === "queue"
          ? "Queue message"
          : "Send message";
  const ariaLabel =
    action === "loading"
      ? "Loading session status"
      : action === "empty"
      ? "Enter a message or attach an image before sending"
      : action === "queue-full"
        ? QUEUE_FULL_SUBMIT_HINT
      : action === "stop"
        ? "Stop current turn"
        : action === "queue"
          ? "Queue message"
          : "Send message";

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <PromptInputSubmit
          aria-disabled={isLoading || isEmpty || isQueueFull}
          aria-label={ariaLabel}
          disabled={isLoading}
          className={cn(
            (isLoading || isEmpty || isQueueFull) &&
              "cursor-not-allowed opacity-50",
          )}
          onClick={(event) => {
            if (isEmpty) {
              event.preventDefault();
              onEmpty();
              return;
            }
            if (isQueueFull) {
              event.preventDefault();
              onQueueFull();
              return;
            }
            if (isStop) {
              event.preventDefault();
              onStop();
            }
          }}
          type={
            isLoading || isEmpty || isQueueFull || isStop ? "button" : "submit"
          }
        >
          {isLoading ? (
            <LoaderCircleIcon
              className="size-4 animate-spin motion-reduce:animate-none"
              aria-hidden="true"
            />
          ) : isStop ? (
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

function ComposerStatusLoading() {
  return (
    <div
      aria-label="Loading session status"
      className={COMPOSER_STATUS_CONTROL_CLASS}
      role="status"
    >
      <LoaderCircleIcon
        className="size-3 animate-spin motion-reduce:animate-none"
        aria-hidden="true"
      />
      status
    </div>
  );
}

function ReadOnlySessionBar({ data }: { data: SessionShowResponse }) {
  return (
    <div className="flex min-h-[52px] flex-wrap items-center gap-3 rounded-md border bg-muted/50 px-3 py-2 text-sm">
      <div className="min-w-0 flex-1 text-muted-foreground">
        {sessionReadOnlyMessage(data)}
      </div>
    </div>
  );
}

function ContextUsageLabel({
  usage,
  activeContext,
  tokenUsage,
}: {
  usage?: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
  tokenUsage: TokenUsage;
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={COMPOSER_STATUS_CONTROL_CLASS}
          aria-label={`Open context usage: ${runtimeContextPercentLabel(usage)}`}
        >
          <CircleGaugeIcon className="size-3" aria-hidden="true" />
          context {runtimeContextPercentLabel(usage)}
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="block max-h-[24rem] max-w-[calc(100vw-2rem)] space-y-1.5 overflow-auto font-mono text-xs"
      >
        {usage ? (
          <ContextUsageTooltip
            usage={usage}
            activeContext={activeContext}
            tokenUsage={tokenUsage}
          />
        ) : (
          <>
            <div>No context usage yet</div>
            <TokenUsageTooltipLine usage={tokenUsage} />
            <ActiveContextDebugLine snapshot={activeContext} />
          </>
        )}
      </PopoverContent>
    </Popover>
  );
}

function ContextUsageTooltip({
  usage,
  activeContext,
  tokenUsage,
}: {
  usage: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
  tokenUsage: TokenUsage;
}) {
  const windowTokens = usage.context_window ?? 0;
  return (
    <>
      <div>{runtimeContextModelLabel(usage)}</div>
      <div>{runtimeContextWindowDetailLabel(usage)}</div>
      <TokenUsageTooltipLine usage={tokenUsage} />
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

function TokenUsageTooltipLine({ usage }: { usage: TokenUsage }) {
  return <div>{runtimeTokenUsageDetailLabel(usage)}</div>;
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
      ~{formatTokenCount(tokens)} estimated tokens
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

function AssistantWorkGroupView({
  work,
  modelLabel,
}: {
  work: AssistantWorkItem;
  modelLabel?: string;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const content = work.contentGroup;
  const copyText = content ? messageGroupCopyText(content) : "";
  const canCopy = content ? messageGroupCanCopy(content) : false;

  return (
    <Message from="assistant">
      <div className="flex w-full flex-col gap-2">
        {modelLabel ? (
          <span className="font-mono text-[11px] text-muted-foreground">
            {modelLabel}
          </span>
        ) : null}
        <details
          open={isOpen}
          onToggle={(event) => setIsOpen(event.currentTarget.open)}
          className="group/work-row w-full"
        >
          <summary className={processDisclosureSummaryClassName()}>
            <span className="min-w-0 truncate">
              {assistantWorkTitle(work)}
            </span>
            <ChevronRightIcon
              className="size-3 shrink-0 transition-transform motion-reduce:transition-none group-open/work-row:rotate-90"
              aria-hidden="true"
            />
          </summary>
          <div className={processDisclosureBodyClassName()}>
            {work.processGroups.flatMap((group) =>
              group.units.map((unit, index) => {
                const key = `${group.key}:${index}`;
                if (unit.kind === "reasoning") {
                  return (
                    <ThinkingProcessRow
                      key={key}
                      redacted={unit.block.redacted}
                      text={thinkingProcessVisibleText(unit.block)}
                    />
                  );
                }
                if (unit.kind === "tool_batch") {
                  return (
                    <ToolBatchProcessRow key={key} tools={unit.tools} />
                  );
                }
                return <ToolProcessRow key={key} tool={unit} />;
              }),
            )}
          </div>
        </details>
        {content ? <AssistantWorkContent group={content} /> : null}
        {canCopy ? <MessageCopyAction text={copyText} align="start" /> : null}
      </div>
    </Message>
  );
}

function AssistantWorkContent({ group }: { group: MessageGroup }) {
  return group.units.map((unit, index) => {
    if (unit.kind === "text") {
      return unit.block.text.trim() ? (
        <AssistantPlainText key={index} text={unit.block.text} />
      ) : null;
    }
    if (unit.kind !== "image") return null;
    if (index > 0 && group.units[index - 1]?.kind === "image") return null;
    const media: Array<MediaRef | null> = [];
    for (let cursor = index; cursor < group.units.length; cursor++) {
      const candidate = group.units[cursor];
      if (candidate.kind !== "image") break;
      media.push(candidate.block.media ?? null);
    }
    return <MessageImageGallery key={index} media={media} role="assistant" />;
  });
}

function MessageGroupView({
  group,
  compactCommand,
  modelLabel,
}: {
  group: MessageGroup;
  compactCommand?: string;
  modelLabel?: string;
}) {
  const isMCPEvent = group.role === "user" && group.kind === "mcp_event";
  const isObservationEvent =
    group.role === "user" && group.kind === "observation";
  const isHookEvent = group.kind === "hook_event";
  const isModelFallback =
    group.role === "user" && group.kind === "model_fallback";
  const isSystemNotice =
    group.role === "user" && group.kind === "system_notice";
  const isCompact = group.kind === "compact";
  const isPendingCompact = group.kind === LOCAL_COMPACT_PENDING_KIND;
  const copyText = messageGroupCopyText(group);
  const canCopyMessage = !isPendingCompact && messageGroupCanCopy(group);

  if (isMCPEvent || isObservationEvent) {
    return (
      <ExternalEventGroup
        group={group}
        eventKind={isObservationEvent ? "observation" : "mcp"}
      />
    );
  }

  if (isHookEvent) {
    return <HookEventGroup group={group} />;
  }

  if (isModelFallback) {
    return <ModelFallbackGroup group={group} />;
  }

  if (isSystemNotice) {
    return <SystemNoticeGroup group={group} />;
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
  const userImageMedia =
    group.role === "user"
      ? group.units.flatMap((unit) =>
          unit.kind === "image" ? [unit.block.media ?? null] : [],
        )
      : [];

  return (
    <Message from={group.role}>
      <div className="flex w-full flex-col gap-2">
        {modelLabel ? (
          <span className="font-mono text-[11px] text-muted-foreground">
            {modelLabel}
          </span>
        ) : null}
        {userImageMedia.length > 0 ? (
          <MessageImageGallery media={userImageMedia} role="user" />
        ) : null}
        {group.units.map((unit, i) => {
          if (unit.kind === "text") {
            if (group.role === "assistant") {
              return <AssistantPlainText key={i} text={unit.block.text} />;
            }
            return (
              <MessageContent key={i}>
                <MessageResponse>{unit.block.text}</MessageResponse>
              </MessageContent>
            );
          }
          if (unit.kind === "reasoning") {
            const text = thinkingProcessVisibleText(unit.block);
            return (
              <ThinkingProcessRow
                key={i}
                redacted={unit.block.redacted}
                text={text}
              />
            );
          }
          if (unit.kind === "image") {
            if (group.role === "user") return null;
            if (i > 0 && group.units[i - 1]?.kind === "image") return null;
            const media: Array<MediaRef | null> = [];
            for (let cursor = i; cursor < group.units.length; cursor++) {
              const candidate = group.units[cursor];
              if (candidate.kind !== "image") break;
              media.push(candidate.block.media ?? null);
            }
            return (
              <MessageImageGallery
                key={i}
                media={media}
                role={group.role}
              />
            );
          }
          if (unit.kind === "tool_batch") {
            return <ToolBatchProcessRow key={i} tools={unit.tools} />;
          }
          return <ToolProcessRow key={i} tool={unit} />;
        })}
        {group.pending && isEmpty ? (
          <div className="animate-pulse text-sm text-muted-foreground motion-reduce:animate-none">
            ...
          </div>
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

function MessageImageGallery({
  media,
  role,
}: {
  media: Array<MediaRef | null>;
  role: MessageGroup["role"];
}) {
  if (media.length === 0) return null;
  return (
    <div
      className={cn(
        role === "user"
          ? "flex w-full flex-wrap justify-end gap-2"
          : "grid w-fit max-w-full gap-2",
        role !== "user" && media.length > 1 && "grid-cols-2",
        role === "user" ? "ml-auto" : "mr-auto",
      )}
    >
      {media.map((item, index) => (
        <ImageBlock
          key={`${item?.artifact_path ?? "image"}-${index}`}
          media={item}
          variant={role === "user" ? "thumbnail" : "card"}
          className={
            role !== "user" && media.length > 1
              ? "max-w-[16rem]"
              : undefined
          }
        />
      ))}
    </div>
  );
}

function AssistantPlainText({ text }: { text: string }) {
  return (
    <div className="max-w-[min(100%,42rem)] text-[14.5px] leading-7 text-foreground">
      <AssistantMarkdown>{text}</AssistantMarkdown>
    </div>
  );
}

function ThinkingProcessRow({
  redacted,
  text,
}: {
  redacted?: boolean;
  text: string;
}) {
  const display = thinkingProcessDisplay(text, redacted);

  return (
    <details className="group/thinking-row w-full">
      <summary className={thinkingDisclosureSummaryClassName()}>
        <span className="min-w-0 truncate">Thinking</span>
        <ChevronRightIcon
          className="size-3 shrink-0 transition-transform group-open/thinking-row:rotate-90"
          aria-hidden="true"
        />
      </summary>
      <div className={thinkingDisclosureBodyClassName()}>
        <MessageResponse className="break-words">
          {display.content || "-"}
        </MessageResponse>
      </div>
    </details>
  );
}

function ToolBatchProcessRow({ tools }: { tools: ToolDisplayUnit[] }) {
  const title = formatToolBatchTitle(tools.map(toolProcessName));
  const status = aggregateToolProcessStatus(
    tools.map((tool) => toolState(tool.use, tool.result)),
  );

  return (
    <ProcessDisclosure
      status={status}
      title={title || "tool batch"}
    >
      <div className="flex flex-col gap-1.5">
        {tools.map((tool, index) => (
          <ToolProcessRow
            key={tool.use?.tool_use_id ?? tool.result?.tool_use_id ?? index}
            tool={tool}
            nested
          />
        ))}
      </div>
    </ProcessDisclosure>
  );
}

function ToolProcessRow({
  nested = false,
  tool,
}: {
  nested?: boolean;
  tool: ToolDisplayUnit;
}) {
  const state = toolState(tool.use, tool.result);
  const status = toolProcessStatus(state);
  const name = toolProcessName(tool);
  const hasContent = Boolean(tool.use || tool.result);

  return (
    <ProcessDisclosure
      status={status}
      title={name}
      nested={nested}
      detail={toolTimeoutLabel(tool.use?.timeout_seconds)}
    >
      {hasContent ? (
        <div className="flex flex-col gap-2">
          {tool.use ? (
            <ProcessPayload
              label="Parameters"
              value={formatToolInput(tool.use.input)}
            />
          ) : null}
          {tool.result ? (
            <ToolResultPayload result={tool.result} />
          ) : null}
        </div>
      ) : null}
    </ProcessDisclosure>
  );
}

function ToolResultPayload({ result }: { result: NonNullable<ToolDisplayUnit["result"]> }) {
  const text = formatToolProcessResult(result);
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {text ? (
        <ProcessPayload
          label={result.is_error ? "Error" : "Result"}
          tone={result.is_error ? "error" : "muted"}
          value={text}
        />
      ) : null}
      {result.media ? <ImageBlock media={result.media} /> : null}
      {!text && !result.media ? (
        <ProcessPayload
          label={result.is_error ? "Error" : "Result"}
          tone={result.is_error ? "error" : "muted"}
          value="-"
        />
      ) : null}
    </div>
  );
}

function ProcessDisclosure({
  children,
  detail,
  nested = false,
  status,
  title,
}: {
  children: ReactNode;
  detail?: string;
  nested?: boolean;
  status: ToolProcessStatus;
  title: string;
}) {
  const [isOpen, setIsOpen] = useState(false);

  return (
    <details
      open={isOpen}
      onToggle={(event) => setIsOpen(event.currentTarget.open)}
      className={processDisclosureClassName(nested)}
    >
      <summary className={processDisclosureSummaryClassName()}>
        <ProcessStatusIndicator status={status} />
        <span className="sr-only">{toolProcessStatusLabel(status)}</span>
        <span className="min-w-0 truncate">{title}</span>
        {detail ? (
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
            {detail}
          </span>
        ) : null}
        <ChevronRightIcon
          className={processDisclosureChevronClassName(nested)}
          aria-hidden="true"
        />
      </summary>
      <div className={processDisclosureBodyClassName()}>{children}</div>
    </details>
  );
}

function ProcessStatusIndicator({ status }: { status: ToolProcessStatus }) {
  if (status === "running") {
    return (
      <LoaderCircleIcon
        className="size-3 shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none"
        aria-hidden="true"
      />
    );
  }
  return (
    <span
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-full",
        status === "failed"
          ? "bg-juex-error-bg text-juex-error"
          : "bg-juex-success-bg text-juex-done",
      )}
      aria-hidden="true"
    >
      {status === "failed" ? (
        <CircleAlertIcon className="size-3" />
      ) : (
        <CheckIcon className="size-3" />
      )}
    </span>
  );
}

function ProcessPayload({
  label,
  tone = "muted",
  value,
}: {
  label: string;
  tone?: "muted" | "error";
  value: string;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div
        className={cn(
          "font-mono text-[10px] uppercase tracking-normal",
          tone === "error" ? "text-juex-error" : "text-muted-foreground",
        )}
      >
        {label}
      </div>
      <pre
        className={cn(
          "max-h-72 overflow-auto whitespace-pre-wrap break-words rounded border px-2 py-1.5 font-mono text-[11px] leading-relaxed",
          tone === "error"
            ? "border-juex-error/25 bg-juex-error-bg/40 text-juex-error"
            : "border-border/60 bg-muted/35 text-foreground",
        )}
      >
        {value}
      </pre>
    </div>
  );
}

function toolProcessName(tool: ToolDisplayUnit): string {
  const raw = tool.use?.tool_name ?? "tool";
  return toolDisplayName(`tool-${raw}`);
}

function formatToolInput(input: Record<string, unknown> | undefined): string {
  if (input === undefined) return "{}";
  try {
    return JSON.stringify(input, null, 2);
  } catch {
    return String(input);
  }
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

function ExternalEventGroup({
  eventKind,
  group,
}: {
  eventKind: "mcp" | "observation";
  group: MessageGroup;
}) {
  const isEmpty = group.units.length === 0;
  return (
    <div className="flex w-full justify-center px-2 py-0.5">
      <div className="flex w-full max-w-[min(34rem,100%)] flex-col gap-2">
        {group.units.map((unit, i) => {
          if (unit.kind !== "text") return null;
          return (
            <ExternalEventMessage
              key={i}
              eventKind={eventKind}
              text={unit.block.text}
            />
          );
        })}
        {group.pending && isEmpty ? (
          <div className="text-center text-sm text-muted-foreground">...</div>
        ) : null}
      </div>
    </div>
  );
}

function HookEventGroup({ group }: { group: MessageGroup }) {
  const text = group.units
    .filter((unit) => unit.kind === "text")
    .map((unit) => (unit.kind === "text" ? unit.block.text : ""))
    .filter(Boolean)
    .join("\n");
  if (!text && !group.pending) return null;
  return (
    <div className="flex w-full justify-center px-2 py-0.5">
      <div
        className="max-w-full truncate rounded-full bg-muted/60 px-2.5 py-1 font-mono text-[11px] text-muted-foreground"
        title={text}
      >
        {text || "..."}
      </div>
    </div>
  );
}

function SystemNoticeGroup({ group }: { group: MessageGroup }) {
  const text = group.units
    .filter((unit) => unit.kind === "text")
    .map((unit) => (unit.kind === "text" ? unit.block.text : ""))
    .filter(Boolean)
    .join("\n");
  if (!text && !group.pending) return null;
  return (
    <div
      className="flex w-full justify-center px-2 py-0.5"
      data-system-notice-message
    >
      <div className="w-full max-w-[min(42rem,100%)]">
        <ProcessDisclosure status="done" title="Automated notice">
          <MessageResponse className="break-words text-[13px] leading-6 text-muted-foreground">
            {text || "..."}
          </MessageResponse>
        </ProcessDisclosure>
      </div>
    </div>
  );
}

function ModelFallbackGroup({ group }: { group: MessageGroup }) {
  const text = group.units
    .filter((unit) => unit.kind === "text")
    .map((unit) => (unit.kind === "text" ? unit.block.text : ""))
    .filter(Boolean)
    .join("\n");
  if (!text && !group.pending) return null;
  const display = formatModelFallbackNotice(text);
  return (
    <div
      className="flex w-full justify-center px-2 py-0.5"
      data-model-fallback-message
    >
      <div className="w-full max-w-[min(42rem,100%)]">
        <ProcessDisclosure status="done" title={display.title}>
          <MessageResponse className="break-words text-[13px] leading-6 text-muted-foreground">
            {display.content || "..."}
          </MessageResponse>
        </ProcessDisclosure>
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

function ExternalEventMessage({
  eventKind,
  text,
}: {
  eventKind: "mcp" | "observation";
  text: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const event = useMemo(
    () =>
      eventKind === "observation"
        ? formatObservationEventForDisplay(text)
        : formatMCPEventForDisplay(text),
    [eventKind, text],
  );
  const eventName =
    eventKind === "observation" ? "observation event" : "MCP event";
  const toggleLabel = expanded ? `Collapse ${eventName}` : `Expand ${eventName}`;

  return (
    <details
      open={expanded}
      onToggle={(event) => setExpanded(event.currentTarget.open)}
      className="group/external-event w-full"
      data-external-event-message
      data-external-event-kind={eventKind}
      data-mcp-event-message={eventKind === "mcp" ? "" : undefined}
    >
      <summary
        className={externalEventRowClassName()}
        title={toggleLabel}
        data-external-event-toggle
        data-mcp-event-toggle={eventKind === "mcp" ? "" : undefined}
      >
        <RadioIcon className="size-3.5 shrink-0" aria-hidden="true" />
        <span
          className="min-w-0 max-w-[48%] shrink-0 truncate font-mono font-semibold sm:max-w-[18rem]"
          data-external-event-label
          data-mcp-event-label={eventKind === "mcp" ? "" : undefined}
        >
          {event.label}
        </span>
        <span
          className="size-1 shrink-0 rounded-full bg-current opacity-45"
          aria-hidden="true"
        />
        <span
          className="min-w-0 flex-1 truncate text-[12px] text-current opacity-75"
          data-external-event-preview
          data-mcp-event-preview={eventKind === "mcp" ? "" : undefined}
        >
          {event.preview}
        </span>
        <ChevronRightIcon
          className="size-3.5 shrink-0 transition-transform group-open/external-event:rotate-90"
          aria-hidden="true"
        />
      </summary>
      {expanded ? (
        <div
          className={externalEventBodyClassName()}
          data-external-event-body
          data-mcp-event-body={eventKind === "mcp" ? "" : undefined}
        >
          <span
            data-external-event-copy
            data-mcp-event-copy={eventKind === "mcp" ? "" : undefined}
          >
            <CopyTextButton
              text={event.copyText}
              className={externalEventCopyClassName()}
              copiedTooltip="Copied to clipboard"
              idleTooltip="Copy event content"
              label="Copy event content"
              size="icon-sm"
            />
          </span>
          <MessageResponse className="break-words">{event.content}</MessageResponse>
        </div>
      ) : null}
    </details>
  );
}
