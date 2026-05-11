import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { MessageList } from "@/components/MessageList";
import { Composer } from "@/components/Composer";
import type { Status } from "@/components/StatusPill";
import { getSession, interrupt, startTurn, subscribeEvents } from "@/api";
import type { Message, SessionShowResponse } from "@/types";

export function Session() {
  const { id = "" } = useParams<{ id: string }>();
  const [data, setData] = useState<SessionShowResponse | null>(null);
  const [liveMessages, setLiveMessages] = useState<Message[]>([]);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const [scrollRequest, setScrollRequest] = useState<ScrollRequest>({
    version: 0,
    force: false,
  });
  const doneTimerRef = useRef<number | null>(null);

  const scrollToLatest = useCallback((force = false) => {
    setScrollRequest((r) => ({ version: r.version + 1, force }));
  }, []);

  // refresh is stable per id; both effects depend on it via [id].
  const refresh = useCallback(async () => {
    try {
      const r = await getSession(id);
      setData(r);
      setLiveMessages([]);
      scrollToLatest();
    } catch (e) {
      console.error("getSession failed", e);
    }
  }, [id, scrollToLatest]);

  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    (async () => {
      try {
        const r = await getSession(id);
        if (!cancelled) {
          setData(r);
          setLiveMessages([]);
          scrollToLatest(true);
        }
      } catch (e) {
        if (!cancelled) console.error("getSession failed", e);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [id, scrollToLatest]);

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

  const messages: Message[] = [...(data.messages ?? []), ...liveMessages];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-baseline gap-3 border-b px-6 py-3 text-sm">
        <code className="font-mono text-xs">{data.id}</code>
        <Badge variant="secondary">{data.turns} turns</Badge>
        <span className="text-muted-foreground text-xs">
          last active {new Date(data.last_active_at).toLocaleString()}
        </span>
      </div>
      <div className="flex min-h-0 flex-1 flex-col">
        <MessageList
          messages={messages}
          model={data.model}
          scrollRequest={scrollRequest}
        />
      </div>
      <Composer
        status={status}
        onSend={handleSend}
        onInterrupt={handleInterrupt}
      />
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
    scrollToLatest();
  }

  function applyAssistantResponse(e: { turn_id?: string; payload?: unknown }) {
    if (!e.turn_id) return;
    const blocks = assistantBlocks(e.payload);
    setLiveMessages((prev) => {
      let found = false;
      const next = prev.map((m) => {
        if (m.turn_id === e.turn_id && m.role === "assistant") {
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
    scrollToLatest();
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
    setLiveMessages((prev) => [
      ...prev,
      {
        role: "user",
        turn_id: e.turn_id,
        blocks: [
          {
            type: "tool_result",
            tool_use_id: toolUseID,
            content,
            is_error: isError,
          },
        ],
      },
    ]);
    scrollToLatest();
  }
}

type ScrollRequest = {
  version: number;
  force: boolean;
};

function findPendingTurnForInput(
  messages: Message[],
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

function messageText(message: Message): string | undefined {
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

function assistantBlocks(payload: unknown): Message["blocks"] {
  const blocks: NonNullable<Message["blocks"]> = [];
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
