import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation";
import {
  Message,
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
  PromptInputButton,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
} from "@/components/ai-elements/prompt-input";
import { StatusPill, type Status } from "@/components/StatusPill";
import {
  messagesToGroups,
  toolState,
  type MessageGroup,
} from "@/lib/display-units";
import {
  compactSession,
  getSession,
  getSessionContext,
  interrupt,
  startTurn,
  subscribeEvents,
} from "@/api";
import { ArchiveIcon, RadioIcon } from "lucide-react";
import type {
  ActiveContextSnapshot,
  ContextUsage,
  Message as ChatMessage,
  SessionShowResponse,
  TokenUsage,
} from "@/types";

export function Session() {
  const { id = "" } = useParams<{ id: string }>();
  const [data, setData] = useState<SessionShowResponse | null>(null);
  const [liveMessages, setLiveMessages] = useState<ChatMessage[]>([]);
  const [liveTokenUsage, setLiveTokenUsage] = useState<TokenUsage | null>(null);
  const [liveContextUsage, setLiveContextUsage] =
    useState<ContextUsage | null>(null);
  const [activeContext, setActiveContext] =
    useState<ActiveContextSnapshot | null>(null);
  const [isCompacting, setIsCompacting] = useState(false);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const doneTimerRef = useRef<number | null>(null);

  // refresh is stable per id; both effects depend on it via [id].
  const refresh = useCallback(async () => {
    try {
      const r = await getSession(id);
      setData(r);
      setLiveMessages([]);
      setLiveTokenUsage(null);
      setLiveContextUsage(null);
      try {
        setActiveContext(await getSessionContext(id));
      } catch (contextError) {
        console.error("getSessionContext failed", contextError);
        setActiveContext(null);
      }
    } catch (e) {
      console.error("getSession failed", e);
    }
  }, [id]);

  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    (async () => {
      try {
        const r = await getSession(id);
        if (!cancelled) {
          setData(r);
          setLiveMessages([]);
          setLiveTokenUsage(null);
          setLiveContextUsage(null);
          try {
            setActiveContext(await getSessionContext(id));
          } catch (contextError) {
            console.error("getSessionContext failed", contextError);
            setActiveContext(null);
          }
        }
      } catch (e) {
        if (!cancelled) console.error("getSession failed", e);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [id]);

  // SSE subscription.
  useEffect(() => {
    if (!id) return;
    const unsub = subscribeEvents(id, {
      onEvent: (e) => {
        switch (e.type) {
          case "turn.started":
            appendLiveTurn(e.turn_id, eventInput(e), eventKind(e), "event");
            setStatus({ kind: "running" });
            break;
          case "llm.requested":
            setStatus({ kind: "running" });
            break;
          case "llm.responded":
            applyAssistantResponse(e);
            applyTokenUsage(e);
            applyContextUsage(e);
            setStatus({ kind: "running" });
            break;
          case "tool.requested": {
            const name =
              eventString(e, "name") ?? eventString(e, "tool_name") ?? "?";
            setStatus({ kind: "tool", name });
            break;
          }
          case "tool.completed":
            appendToolResult(e, false);
            setStatus({ kind: "running" });
            break;
          case "tool.errored":
            appendToolResult(e, true);
            setStatus({ kind: "running" });
            break;
          case "turn.completed":
            refresh().then(() => {
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
            refresh().then(() =>
              setStatus({ kind: "error", detail: eventErrorDetail(e) }),
            );
            break;
          case "context.compact.started":
            setStatus({ kind: "running" });
            break;
          case "context.compact.completed":
            refresh().then(() => {
              setStatus({ kind: "done" });
              if (doneTimerRef.current)
                window.clearTimeout(doneTimerRef.current);
              doneTimerRef.current = window.setTimeout(
                () => setStatus({ kind: "idle" }),
                1500,
              );
            });
            break;
          case "context.compact.errored":
            refresh().then(() =>
              setStatus({ kind: "error", detail: eventErrorDetail(e) }),
            );
            break;
        }
      },
    });
    return () => {
      unsub();
      if (doneTimerRef.current) window.clearTimeout(doneTimerRef.current);
    };
  }, [id, refresh]);

  async function handleSend(prompt: string) {
    try {
      const turn = await startTurn(id, prompt);
      appendLiveTurn(turn.turn_id, prompt, undefined, "optimistic");
      setStatus({ kind: "running" });
    } catch (e) {
      console.error("startTurn failed", e);
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

  async function handleCompact() {
    if (!id) return;
    setIsCompacting(true);
    setStatus({ kind: "running" });
    try {
      await compactSession(id, "manual");
      await refresh();
      setStatus({ kind: "done" });
      if (doneTimerRef.current) window.clearTimeout(doneTimerRef.current);
      doneTimerRef.current = window.setTimeout(
        () => setStatus({ kind: "idle" }),
        1500,
      );
    } catch (e) {
      console.error("compactSession failed", e);
      setStatus({
        kind: "error",
        detail: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setIsCompacting(false);
    }
  }

  if (!data) {
    return <div className="text-muted-foreground p-8">Loading...</div>;
  }

  const messages: ChatMessage[] = [...(data.messages ?? []), ...liveMessages];
  const groups = messagesToGroups(messages);
  const tokenUsage = liveTokenUsage ?? data.token_usage;
  const contextUsage = liveContextUsage ?? data.context_usage;
  const busy = status.kind === "running" || status.kind === "tool";

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex items-baseline gap-3 border-b px-6 py-3 text-sm">
        <code className="font-mono text-xs">{data.id}</code>
        <Badge variant="secondary">{data.turns} turns</Badge>
        {data.model ? (
          <Badge variant="outline" className="font-mono text-xs">
            {data.model}
          </Badge>
        ) : null}
        <span className="text-muted-foreground text-xs">
          last active {new Date(data.last_active_at).toLocaleString()}
        </span>
      </header>
      <Conversation className="min-h-0 flex-1">
        <ConversationContent className="mx-auto w-full max-w-3xl">
          {groups.map((group) => (
            <MessageGroupView key={group.key} group={group} />
          ))}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>
      <div className="border-t bg-background/95 px-4 py-3 backdrop-blur">
        <div className="mx-auto w-full max-w-3xl">
          <PromptInput
            onSubmit={async (msg) => {
              const text = msg.text?.trim();
              if (text) await handleSend(text);
            }}
          >
            <PromptInputTextarea placeholder="Type a prompt..." />
            <PromptInputFooter>
              <TooltipProvider>
                <PromptInputTools>
                  <StatusPill status={status} />
                  <ContextUsageLabel
                    usage={contextUsage}
                    activeContext={activeContext}
                  />
                  <TokenUsageLabel usage={tokenUsage} />
                </PromptInputTools>
                {busy ? (
                  <PromptInputButton
                    variant="outline"
                    onClick={() => void handleInterrupt()}
                  >
                    Stop
                  </PromptInputButton>
                ) : (
                  <>
                    <PromptInputButton
                      variant="outline"
                      tooltip="Compact context"
                      disabled={isCompacting}
                      onClick={() => void handleCompact()}
                    >
                      <ArchiveIcon className="size-4" aria-hidden="true" />
                    </PromptInputButton>
                    <PromptInputSubmit />
                  </>
                )}
              </TooltipProvider>
            </PromptInputFooter>
          </PromptInput>
        </div>
      </div>
    </div>
  );

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

  function applyAssistantResponse(e: { turn_id?: string; payload?: unknown }) {
    if (!e.turn_id) return;
    const blocks = assistantBlocks(e.payload);
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
  return eventString(e, "kind");
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

function assistantBlocks(payload: unknown): ChatMessage["blocks"] {
  const blocks: NonNullable<ChatMessage["blocks"]> = [];
  if (!payload || typeof payload !== "object") return blocks;
  const record = payload as Record<string, unknown>;

  if (typeof record.thinking === "string" && record.thinking) {
    blocks.push({ type: "reasoning", text: record.thinking });
  }
  if (typeof record.text === "string" && record.text) {
    blocks.push({ type: "text", text: record.text });
  }
  if (Array.isArray(record.tool_calls)) {
    for (const call of record.tool_calls) {
      if (!call || typeof call !== "object") continue;
      const c = call as Record<string, unknown>;
      blocks.push({
        type: "tool_use",
        tool_use_id: typeof c.tool_use_id === "string" ? c.tool_use_id : "",
        tool_name: typeof c.name === "string" ? c.name : "?",
        input:
          c.input && typeof c.input === "object"
            ? (c.input as Record<string, unknown>)
            : undefined,
      });
    }
  }
  return blocks;
}

function TokenUsageLabel({ usage }: { usage: TokenUsage }) {
  const input = usage?.input_tokens ?? 0;
  const output = usage?.output_tokens ?? 0;
  const total = input + output;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="bg-muted text-muted-foreground inline-flex shrink-0 items-center rounded-full px-2.5 py-1 font-mono text-xs">
          tokens {formatTokenCount(total)}
        </span>
      </TooltipTrigger>
      <TooltipContent>
        {formatTokenCount(input)} in / {formatTokenCount(output)} out
      </TooltipContent>
    </Tooltip>
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
        <span className="bg-muted text-muted-foreground inline-flex shrink-0 items-center rounded-full px-2.5 py-1 font-mono text-xs">
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
      <div className="text-background/75">estimated breakdown</div>
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
    <div className="text-background/75">
      debug: active provider context {count} messages,{" "}
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

function MessageGroupView({ group }: { group: MessageGroup }) {
  const isEmpty = group.units.length === 0;
  // Per-message model (stamped at generation time). Falls back to nothing
  // for older messages that pre-date the persistence change; the header
  // already shows the current session-level model in that case.
  const showModel = group.role === "assistant" && !!group.model;
  const isMCPEvent = group.role === "user" && group.kind === "mcp_event";
  const isCompact = group.kind === "compact";

  return (
    <Message from={isCompact ? "assistant" : group.role}>
      <div className="flex w-full flex-col gap-2">
        {showModel ? (
          <span className="text-muted-foreground font-mono text-xs">
            {group.model}
          </span>
        ) : null}
        {group.units.map((unit, i) => {
          if (unit.kind === "text") {
            if (isCompact) {
              return <CompactMessage key={i} text={unit.block.text} />;
            }
            if (isMCPEvent) {
              return <MCPEventMessage key={i} text={unit.block.text} />;
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
              <ToolHeader type={`tool-${toolName}`} state={state} />
              <ToolContent>
                {unit.use ? <ToolInput input={unit.use.input} /> : null}
                {unit.result ? (
                  <ToolOutput
                    output={
                      unit.result.is_error ? null : (
                        <MessageResponse>{unit.result.content}</MessageResponse>
                      )
                    }
                    errorText={unit.result.is_error ? unit.result.content : undefined}
                  />
                ) : null}
              </ToolContent>
            </Tool>
          );
        })}
        {group.pending && isEmpty ? (
          <div className="text-muted-foreground animate-pulse text-sm">...</div>
        ) : null}
      </div>
    </Message>
  );
}

function CompactMessage({ text }: { text: string }) {
  const summary = parseCompactText(text);
  return (
    <MessageContent className="border border-amber-500/25 bg-amber-50 text-amber-950 dark:border-amber-400/25 dark:bg-amber-950/25 dark:text-amber-50">
      <div className="flex items-center gap-2 text-xs font-medium">
        <ArchiveIcon className="size-3.5" aria-hidden="true" />
        <span>Context compacted</span>
      </div>
      <MessageResponse>{summary}</MessageResponse>
    </MessageContent>
  );
}

function parseCompactText(text: string): string {
  const marker = "Summary of earlier conversation:";
  const markerIndex = text.indexOf(marker);
  if (markerIndex < 0) return text;
  return text.slice(markerIndex + marker.length).trim();
}

function MCPEventMessage({ text }: { text: string }) {
  const event = parseMCPEventText(text);
  return (
    <MessageContent className="group-[.is-user]:border group-[.is-user]:border-teal-500/30 group-[.is-user]:bg-teal-50 group-[.is-user]:text-teal-950 group-[.is-user]:dark:border-teal-400/30 group-[.is-user]:dark:bg-teal-950/30 group-[.is-user]:dark:text-teal-50">
      <div className="flex items-center gap-2 text-xs font-medium">
        <RadioIcon className="size-3.5" aria-hidden="true" />
        <span className="font-mono">{event.label}</span>
      </div>
      <MessageResponse>{event.content}</MessageResponse>
    </MessageContent>
  );
}

function parseMCPEventText(text: string): { label: string; content: string } {
  const first = text.indexOf(":");
  const second = first >= 0 ? text.indexOf(":", first + 1) : -1;
  if (first < 0 || second < 0) {
    return { label: "mcp:event", content: text };
  }
  return {
    label: `${text.slice(0, first)}:${text.slice(first + 1, second)}`,
    content: text.slice(second + 1),
  };
}
