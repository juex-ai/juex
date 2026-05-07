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
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const doneTimerRef = useRef<number | null>(null);

  // refresh is stable per id; both effects depend on it via [id].
  const refresh = useCallback(async () => {
    try {
      const r = await getSession(id);
      setData(r);
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
        if (!cancelled) setData(r);
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
            setStatus({ kind: "running" });
            break;
          case "tool.requested": {
            const name =
              e.payload &&
              typeof e.payload === "object" &&
              "tool_name" in e.payload
                ? String((e.payload as Record<string, unknown>).tool_name)
                : "?";
            setStatus({ kind: "tool", name });
            break;
          }
          case "tool.completed":
          case "tool.errored":
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
            refresh().then(() => setStatus({ kind: "error" }));
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
      await startTurn(id, prompt);
      setStatus({ kind: "running" });
    } catch (e) {
      console.error("startTurn failed", e);
      setStatus({ kind: "error" });
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

  const messages: Message[] = data.messages ?? [];

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
        <MessageList messages={messages} />
      </div>
      <Composer
        status={status}
        onSend={handleSend}
        onInterrupt={handleInterrupt}
      />
    </div>
  );
}
