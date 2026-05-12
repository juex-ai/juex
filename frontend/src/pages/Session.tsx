import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
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
import { messagesToGroups, toolState, type MessageGroup } from "@/lib/display-units";
import { getSession, interrupt, startTurn, subscribeEvents } from "@/api";
import type { Message as ChatMessage, SessionShowResponse } from "@/types";

export function Session() {
  const { id = "" } = useParams<{ id: string }>();
  const [data, setData] = useState<SessionShowResponse | null>(null);
  const [liveMessages, setLiveMessages] = useState<ChatMessage[]>([]);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const doneTimerRef = useRef<number | null>(null);

  // refresh is stable per id; both effects depend on it via [id].
  const refresh = useCallback(async () => {
    try {
      const r = await getSession(id);
      setData(r);
      setLiveMessages([]);
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
            appendLiveTurn(e.turn_id, eventInput(e), "event");
            setStatus({ kind: "running" });
            break;
          case "llm.requested":
            setStatus({ kind: "running" });
            break;
          case "llm.responded":
            applyAssistantResponse(e);
            setStatus({ kind: "running" });
            break;
          case "tool.requested": {
            const name = eventString(e, "name") ?? eventString(e, "tool_name") ?? "?";
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
      appendLiveTurn(turn.turn_id, prompt, "optimistic");
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

  if (!data) {
    return <div className="text-muted-foreground p-8">Loading...</div>;
  }

  const messages: ChatMessage[] = [...(data.messages ?? []), ...liveMessages];
  const groups = messagesToGroups(messages);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex items-baseline gap-3 border-b px-6 py-3 text-sm">
        <code className="font-mono text-xs">{data.id}</code>
        <Badge variant="secondary">{data.turns} turns</Badge>
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
              <PromptInputTools>
                <StatusPill status={status} />
              </PromptInputTools>
              {status.kind === "running" || status.kind === "tool" ? (
                <PromptInputButton
                  variant="outline"
                  onClick={() => void handleInterrupt()}
                >
                  Stop
                </PromptInputButton>
              ) : (
                <PromptInputSubmit />
              )}
            </PromptInputFooter>
          </PromptInput>
        </div>
      </div>
    </div>
  );

  function appendLiveTurn(
    turnID: string | undefined,
    input: string | undefined,
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
    setLiveMessages((prev) => {
      let found = false;
      const next = prev.map((m) => {
        if (m.turn_id === e.turn_id && m.role === "assistant" && m.pending) {
          found = true;
          return { ...m, pending: false, blocks };
        }
        return m;
      });
      if (found) return next;
      return [
        ...next,
        { role: "assistant", turn_id: e.turn_id, pending: false, blocks },
      ];
    });
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

function MessageGroupView({ group }: { group: MessageGroup }) {
  const isEmpty = group.units.length === 0;

  return (
    <Message from={group.role}>
      <div className="flex w-full flex-col gap-2">
        {group.units.map((unit, i) => {
          if (unit.kind === "text") {
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
                <Reasoning key={i} isStreaming={false} defaultOpen>
                  <ReasoningTrigger>Thinking [redacted]</ReasoningTrigger>
                  <ReasoningContent>
                    [redacted by provider]
                  </ReasoningContent>
                </Reasoning>
              );
            }
            return (
              <Reasoning key={i} isStreaming={false} defaultOpen>
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
