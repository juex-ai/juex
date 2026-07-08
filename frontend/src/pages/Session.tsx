import {
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
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
  type ToolDisplayUnit,
} from "@/lib/display-units";
import {
  COMPACTING_SUBMIT_HINT,
  composerSubmitAction,
  type ComposerSubmitAction,
} from "@/lib/composer-submit";
import {
  isCompactCommandInput,
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
  formatToolProcessResultText,
  thinkingProcessDisplay,
  toolDisplayName,
  toolProcessStatus,
  toolProcessStatusLabel,
  type ToolProcessStatus,
} from "@/lib/tool-display";
import { sessionPreviewTitle } from "@/lib/session-title";
import {
  formatMCPEventForDisplay,
  formatObservationEventForDisplay,
} from "@/lib/mcp-events";
import {
  externalEventBodyClassName,
  externalEventCopyClassName,
  externalEventRowClassName,
  processDisclosureBodyClassName,
  processDisclosureChevronClassName,
  processDisclosureClassName,
  processDisclosureDefaultOpen,
  processDisclosureSummaryClassName,
  processStatusDotClassName,
  thinkingDisclosureBodyClassName,
  thinkingDisclosureSummaryClassName,
} from "@/lib/message-rendering";
import { cn } from "@/lib/utils";
import {
  WORKING_STATE_SECTIONS,
  formatRuntimeTimestamp,
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeGoalContinuationLabel,
  runtimeSessionStateBadgeLabel,
  runtimeSessionStateIsActive,
  runtimeTokenUsageDetailLabel,
  workingStatePresenceLabel,
  workingStateRecords,
  workingStateSectionCounts,
} from "@/lib/runtime-display";
import { QueuedInputStack } from "@/components/QueuedInputStack";
import { Separator } from "@/components/ui/separator";
import {
  clearComposerHint,
  createSessionReadState,
  markSessionProjectionIdle,
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
  projectTurnStatus,
  resetSessionReadState,
  type SessionInitialCommandState,
  type SessionReadEffect,
  type SessionReadResult,
  type SessionReadState,
} from "@/lib/session-read-state";
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
  ChevronRightIcon,
  ChevronUpIcon,
  CircleGaugeIcon,
  CopyIcon,
  LoaderCircleIcon,
  RadioIcon,
  SendHorizontalIcon,
  SquareIcon,
} from "lucide-react";
import type {
  ActiveContextSnapshot,
  ContextUsage,
  GoalStatusSnapshot,
  Message as ChatMessage,
  SessionShowResponse,
  TokenUsage,
  WorkingStateRecord,
  WorkingStateStatusSnapshot,
} from "@/types";

type InitialCommandState = SessionInitialCommandState;

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
  const [readState, setReadState] = useState<SessionReadState>(() =>
    createSessionReadState(),
  );
  const [draft, setDraft] = useState("");
  const doneTimerRef = useRef<number | null>(null);
  const composerHintTimerRef = useRef<number | null>(null);
  const initialCommandRef = useRef<string | null>(null);
  const readStateRef = useRef<SessionReadState | null>(null);
  if (readStateRef.current === null) {
    readStateRef.current = readState;
  }
  const latestRouteRef = useRef({ id });
  const {
    data,
    loadError,
    projection,
    activeContext,
    composerHint,
    loadingOlderMessages,
    olderMessagesError,
  } = readState;

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
      updateReadState((prev) => projectSessionLoaded(prev, r, opts));
      try {
        const context = await getSessionContext(requestId);
        if (!isLatestRoute(latestRouteRef.current, requestId)) {
          return;
        }
        updateReadState((prev) => projectActiveContextLoaded(prev, context));
      } catch (contextError) {
        if (isLatestRoute(latestRouteRef.current, requestId)) {
          console.error("getSessionContext failed", contextError);
          updateReadState(projectActiveContextFailed);
        }
      }
    } catch (e) {
      if (isLatestRoute(latestRouteRef.current, requestId)) {
        console.error("getSession failed", e);
      }
    }
    // updateReadState writes through readStateRef and React's stable setter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    const state = location.state as InitialCommandState;
    const activeTurnID = state?.activeTurnID;
    setSessionReadState(
      resetSessionReadState(currentReadState(), { activeTurnID }),
    );
    setDraft("");
    if (!activeTurnID) return;

    let cancelled = false;
    let timer: number | null = null;
    const reconcile = async () => {
      try {
        const turn = await getTurnStatus(id, activeTurnID);
        if (cancelled || !isLatestRoute(latestRouteRef.current, id)) return;
        runSessionReadResult(projectTurnStatus(currentReadState(), turn));
        if (turn.state === "running") {
          timer = window.setTimeout(() => void reconcile(), 1000);
        }
      } catch (e) {
        if (!cancelled && isLatestRoute(latestRouteRef.current, id)) {
          console.error("getTurnStatus failed", e);
          timer = window.setTimeout(() => void reconcile(), 1000);
        }
      }
    };
    void reconcile();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
    // Projection effect helpers read current state from refs; including them
    // would restart the polling loop on every render. location.state is read
    // only on session entry; clearing it later must not reset live projection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, refresh]);

  const loadedTurnID =
    data?.turn?.state === "running" ? data.turn.turn_id : undefined;

  useEffect(() => {
    if (!id || !loadedTurnID) return;

    let cancelled = false;
    let timer: number | null = null;
    const reconcile = async () => {
      try {
        const turn = await getTurnStatus(id, loadedTurnID);
        if (cancelled || !isLatestRoute(latestRouteRef.current, id)) return;
        runSessionReadResult(projectTurnStatus(currentReadState(), turn));
        if (turn.state === "running") {
          timer = window.setTimeout(() => void reconcile(), 1000);
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
    // Projection effect helpers read current state from refs; including them
    // would restart the polling loop on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, loadedTurnID]);

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
        updateReadState((prev) =>
          projectSessionLoaded(prev, r, { preserveLiveMessages: true }),
        );
        try {
          const context = await getSessionContext(requestId);
          if (cancelled || !isLatestRoute(latestRouteRef.current, requestId)) {
            return;
          }
          updateReadState((prev) => projectActiveContextLoaded(prev, context));
        } catch (contextError) {
          if (!cancelled && isLatestRoute(latestRouteRef.current, requestId)) {
            console.error("getSessionContext failed", contextError);
            updateReadState(projectActiveContextFailed);
          }
        }
      } catch (e) {
        if (!cancelled && isLatestRoute(latestRouteRef.current, requestId)) {
          console.error("getSession failed", e);
          updateReadState((prev) => projectSessionLoadFailed(prev, e));
        }
      }
    })();
    return () => {
      cancelled = true;
    };
    // updateReadState writes through readStateRef; including it would refetch
    // the session whenever the route-local controller state changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    if (!data) return;
    const state = location.state as InitialCommandState;
    if (!state?.command || !state.commandInput) return;
    const command = state.command;
    const commandInput = state.commandInput;
    const key = `${id}:${commandInput}:${command.name}:${command.text}`;
    if (initialCommandRef.current === key) return;
    initialCommandRef.current = key;
    runSessionReadResult(
      projectInitialCommand(currentReadState(), commandInput, command),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
        runSessionReadResult(projectLiveBrowserEvent(currentReadState(), e));
      },
    });
    return () => {
      unsub();
      if (doneTimerRef.current) window.clearTimeout(doneTimerRef.current);
      if (composerHintTimerRef.current)
        window.clearTimeout(composerHintTimerRef.current);
    };
    // Projection reads from refs; resubscribing on every live-state change would
    // reopen the EventSource during active turns.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data?.kind, data?.active, id, refresh]);

  async function handleSend(prompt: string) {
    const compactCommand = isCompactCommandInput(prompt);
    updateReadState((prev) => projectPendingSubmit(prev, prompt));
    try {
      const turn = await startTurn(id, prompt);
      runSessionReadResult(
        projectStartTurnSucceeded(currentReadState(), prompt, turn),
      );
    } catch (e) {
      console.error("startTurn failed", e);
      runSessionReadResult(
        projectStartTurnFailed(currentReadState(), compactCommand, e),
      );
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
    if (loadError) {
      return (
        <SessionLoadErrorState
          detail={loadError}
          onHistory={() => navigate("/history")}
        />
      );
    }
    return <LoadingState label="Loading conversation" />;
  }

  const messages: ChatMessage[] = [...(data.messages ?? []), ...projection.messages];
  const groups = messagesToGroups(messages);
  const tokenUsage = projection.tokenUsage ?? data.token_usage;
  const contextUsage = projection.contextUsage ?? data.context_usage;
  const canSend = sessionCanSend(data);
  const submitAction = composerSubmitAction({
    turnActive: projection.turnActive,
    compactActive: projection.compactActive,
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
                group.id ? projection.compactCommandInputs[group.id] : undefined
              }
            />
          ))}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>
      <div className="border-t bg-background/92 px-4 py-3 backdrop-blur md:px-6">
        <div className="mx-auto w-full max-w-[760px]">
          <QueuedInputStack items={projection.queuedInput.items} />
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
                  updateReadState(projectPromptInputChanged);
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
                    {projection.status.kind === "error" ? (
                      <ComposerFeedback tone="error">
                        {projection.status.detail ?? "Something went wrong"}
                      </ComposerFeedback>
                    ) : null}
                    <ContextUsageLabel
                      usage={contextUsage}
                      activeContext={activeContext}
                      tokenUsage={tokenUsage}
                    />
                    <SessionRuntimeStateBadges data={data} />
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
    updateReadState(projectLoadOlderStarted);
    try {
      const page = await getSession(requestId, { before });
      if (!isLatestRoute(latestRouteRef.current, requestId)) {
        return;
      }
      updateReadState((prev) => projectLoadOlderSucceeded(prev, page));
    } catch (error) {
      if (isLatestRoute(latestRouteRef.current, requestId)) {
        updateReadState((prev) => projectLoadOlderFailed(prev, error));
      }
    }
  }

  function setSessionReadState(next: SessionReadState) {
    readStateRef.current = next;
    setReadState(next);
  }

  function currentReadState(): SessionReadState {
    return readStateRef.current ?? readState;
  }

  function updateReadState(project: (state: SessionReadState) => SessionReadState) {
    setSessionReadState(project(currentReadState()));
  }

  function runSessionReadResult(result: SessionReadResult) {
    setSessionReadState(result.state);
    runSessionReadEffects(result.effects);
  }

  function runSessionReadEffects(effects: SessionReadEffect[]) {
    for (const effect of effects) {
      if (effect.type === "refresh") {
        void refresh({ preserveLiveMessages: effect.preserveLiveMessages });
        continue;
      }
      if (effect.type === "scheduleComposerHintClear") {
        scheduleComposerHintClear();
        continue;
      }
      if (effect.type === "clearRouteState") {
        navigate(location.pathname, { replace: true, state: null });
        continue;
      }
      if (effect.type === "dispatchSessionsChanged") {
        window.dispatchEvent(new Event("juex:sessions-changed"));
        continue;
      }
      if (effect.type === "navigateToSession") {
        navigate(`/sessions/${encodeURIComponent(effect.sessionID)}`, {
          state: effect.state,
        });
        continue;
      }
      if (effect.type === "scheduleIdleStatus") {
        scheduleIdleStatus();
      }
    }
  }

  function scheduleIdleStatus() {
    if (doneTimerRef.current) window.clearTimeout(doneTimerRef.current);
    doneTimerRef.current = window.setTimeout(
      () => updateReadState(markSessionProjectionIdle),
      1500,
    );
  }

  function showComposerHint(message: string) {
    runSessionReadResult(projectComposerHint(currentReadState(), message));
  }

  function scheduleComposerHintClear() {
    if (composerHintTimerRef.current) {
      window.clearTimeout(composerHintTimerRef.current);
    }
    composerHintTimerRef.current = window.setTimeout(
      () => updateReadState(clearComposerHint),
      1800,
    );
  }
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

function SessionRuntimeStateBadges({ data }: { data: SessionShowResponse }) {
  return (
    <SessionStateBadge
      label={runtimeSessionStateBadgeLabel()}
      tone={
        runtimeSessionStateIsActive(data.goal, data.working_state)
          ? "active"
          : "muted"
      }
    >
      <SessionStateTooltip goal={data.goal} snapshot={data.working_state} />
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
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          className={cn(
            "inline-flex shrink-0 items-center rounded-full border px-2.5 py-1 font-mono text-[11px]",
            tone === "active"
              ? "border-primary/30 bg-primary/10 text-primary"
              : "border-transparent bg-muted text-muted-foreground",
          )}
          type="button"
        >
          {label}
        </button>
      </TooltipTrigger>
      <TooltipContent
        hideArrow
        className="block !w-[min(34rem,calc(100vw-2rem))] !max-w-[calc(100vw-2rem)] max-h-[24rem] overflow-auto border border-border bg-popover px-3 py-2 text-left text-xs text-popover-foreground shadow-lg"
      >
        {children}
      </TooltipContent>
    </Tooltip>
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
      <RuntimeTooltipRow
        label="criteria"
        value={runtimeListValue(goal.acceptance_criteria)}
      />
      <RuntimeTooltipRow
        label="artifacts"
        value={runtimeListValue(goal.required_artifacts)}
      />
      <RuntimeTooltipRow
        label="artifact rules"
        value={runtimeListValue(goal.artifact_requirements)}
      />
      <RuntimeTooltipRow
        label="validation"
        value={runtimeListValue(goal.validation_requirements)}
      />
      <RuntimeTooltipRow
        label="verification"
        value={goal.verification_method || "-"}
      />
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
  snapshot,
}: {
  goal?: GoalStatusSnapshot;
  snapshot?: WorkingStateStatusSnapshot;
}) {
  return (
    <div className="space-y-3">
      <GoalStateTooltip goal={goal} />
      <div className="border-t border-border/60 pt-3">
        <WorkingStateTooltip snapshot={snapshot} />
      </div>
    </div>
  );
}

function WorkingStateTooltip({
  snapshot,
}: {
  snapshot?: WorkingStateStatusSnapshot;
}) {
  if (!snapshot) {
    return (
      <RuntimeTooltipPanel title="Working State">
        <div className="text-muted-foreground">No active working-state snapshot for this session.</div>
      </RuntimeTooltipPanel>
    );
  }
  const counts = workingStateSectionCounts(snapshot);
  const state = snapshot.state;
  return (
    <RuntimeTooltipPanel title="Working State">
      <RuntimeTooltipRow label="status" value={workingStatePresenceLabel(snapshot)} />
      <RuntimeTooltipRow label="path" value={snapshot.path || "-"} />
      <RuntimeTooltipRow label="updated" value={formatRuntimeTimestamp(state.updated_at)} />
      <RuntimeTooltipRow
        label="counts"
        value={counts.map((item) => `${item.label}: ${item.count}`).join(", ")}
      />
      {WORKING_STATE_SECTIONS.map((section) => {
        const records = workingStateRecords(state, section.key);
        if (records.length === 0) return null;
        return (
          <RuntimeTooltipRecords
            key={section.key}
            title={section.label}
            records={records}
          />
        );
      })}
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
      <div className="font-mono text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
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

function runtimeListValue(values?: string[]): string {
  const text = values?.map((value) => value.trim()).filter(Boolean).join("; ");
  return text || "-";
}

function RuntimeTooltipRecords({
  records,
  title,
}: {
  records: WorkingStateRecord[];
  title: string;
}) {
  return (
    <div className="border-t border-border/60 pt-2">
      <div className="mb-1 font-mono text-[11px] text-muted-foreground">{title}</div>
      <div className="space-y-1.5">
        {records.map((record, index) => (
          <div key={record.id || `${title}:${index}`} className="rounded border border-border/60 bg-background/70 px-2 py-1.5">
            <div className="break-words text-foreground">{record.text || "-"}</div>
            <div className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 font-mono text-[10px] text-muted-foreground">
              {record.source ? <span>source: {record.source}</span> : null}
              {record.severity ? <span>severity: {record.severity}</span> : null}
              {record.confidence != null ? (
                <span>confidence: {formatConfidence(record.confidence)}</span>
              ) : null}
              {record.related_paths && record.related_paths.length > 0 ? (
                <span>paths: {record.related_paths.join(", ")}</span>
              ) : null}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function formatConfidence(value: number): string {
  if (!Number.isFinite(value)) return "-";
  return `${Math.round(value * 100)}%`;
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
  tokenUsage,
}: {
  usage?: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
  tokenUsage: TokenUsage;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-transparent bg-muted px-2.5 py-1 font-mono text-[11px] text-muted-foreground">
          <CircleGaugeIcon className="size-3" aria-hidden="true" />
          context {runtimeContextPercentLabel(usage)}
        </span>
      </TooltipTrigger>
      <TooltipContent className="block max-w-sm space-y-1.5 px-3 py-2 font-mono text-xs">
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
      </TooltipContent>
    </Tooltip>
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
  const isObservationEvent =
    group.role === "user" && group.kind === "observation";
  const isHookEvent = group.kind === "hook_event";
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
            const text = unit.block.text ?? unit.block.content ?? "";
            return (
              <ThinkingProcessRow
                key={i}
                redacted={unit.block.redacted}
                text={text}
              />
            );
          }
          if (unit.kind === "tool_batch") {
            return <ToolBatchProcessRow key={i} tools={unit.tools} />;
          }
          return <ToolProcessRow key={i} tool={unit} />;
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

function AssistantPlainText({ text }: { text: string }) {
  return (
    <div className="max-w-[min(100%,42rem)] text-[14.5px] leading-7 text-foreground">
      <MessageResponse>{text}</MessageResponse>
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
            <ProcessPayload
              label={tool.result.is_error ? "Error" : "Result"}
              tone={tool.result.is_error ? "error" : "muted"}
              value={
                tool.result.content
                  ? formatToolProcessResultText(tool.result.content)
                  : "-"
              }
            />
          ) : null}
        </div>
      ) : null}
    </ProcessDisclosure>
  );
}

function ProcessDisclosure({
  children,
  nested = false,
  status,
  title,
}: {
  children: ReactNode;
  nested?: boolean;
  status: ToolProcessStatus;
  title: string;
}) {
  const [isOpen, setIsOpen] = useState(processDisclosureDefaultOpen);

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
        className="size-3 shrink-0 animate-spin text-muted-foreground"
        aria-hidden="true"
      />
    );
  }
  return (
    <span
      className={processStatusDotClassName(
        status === "failed" ? "failed" : "done",
      )}
      aria-hidden="true"
    />
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
