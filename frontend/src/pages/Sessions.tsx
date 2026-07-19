import { LogoMark } from "@/components/LogoMark";
import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { createSession, listSessions, startTurn } from "@/api";
import {
  PromptInput,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
} from "@/components/ai-elements/prompt-input";
import { useShellTitle } from "@/components/AppShell";
import { AgentRuntimeStateBar } from "@/components/fleet/AgentRuntimeStateBar";
import { useFleetAgent } from "@/components/fleet/FleetAgentContext";
import { Button } from "@/components/ui/button";
import { homeActiveSessionHref } from "@/lib/home-route";
import { agentPathFromLocation } from "@/lib/fleet-routes";
import type { SessionInfo } from "@/types";

export function Sessions() {
  const navigate = useNavigate();
  const location = useLocation();
  const { agent, agentsLoaded } = useFleetAgent();
  const [checkingSession, setCheckingSession] = useState(true);
  const [data, setData] = useState<SessionInfo[] | null>(null);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useShellTitle(null);

  useEffect(() => {
    let live = true;
    setCheckingSession(true);
    setData(null);
    setError(null);
    listSessions()
      .then(({ sessions }) => {
        if (!live) return;
        setData(sessions);
        const href = homeActiveSessionHref(sessions, location.pathname);
        if (href) {
          navigate(href, { replace: true });
        }
      })
      .catch((e) => {
        if (!live) return;
        console.error("listSessions failed", e);
        setError(
          e instanceof Error ? e.message : "Failed to load existing chats.",
        );
      })
      .finally(() => {
        if (live) setCheckingSession(false);
      });
    return () => {
      live = false;
    };
  }, [loadAttempt, location.pathname, navigate]);

  if (error && !data) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center px-4 py-8">
        <div className="flex max-w-md flex-col items-center gap-3 text-center">
          <div
            role="alert"
            className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setLoadAttempt((attempt) => attempt + 1)}
          >
            Retry
          </Button>
        </div>
      </div>
    );
  }

  if (!data || checkingSession) {
    return null;
  }

  return (
    <div className="flex flex-1 items-center justify-center px-4 py-8 text-muted-foreground sm:p-8">
      <div className="flex w-full max-w-[760px] flex-col items-center text-center">
        <LogoMark className="mb-4 size-14 text-primary" />
        <p className="font-serif text-2xl italic leading-tight text-primary sm:text-3xl">
          Aware, action
        </p>
        <div className="mt-6 w-full">
          {agentsLoaded && agent && agent.runtime_health !== "healthy" ? (
            <AgentRuntimeStateBar />
          ) : (
            <PromptInput
              onSubmit={async (msg) => {
                const text = msg.text?.trim();
                if (!text) return;
                setSending(true);
                setError(null);
                try {
                  const session = await createSession();
                  const turn = await startTurn(session.id, text);
                  const targetSessionID =
                    turn.command?.name === "/new" &&
                    turn.command.status?.session_id
                      ? turn.command.status.session_id
                      : session.id;
                  navigate(
                    agentPathFromLocation(
                      `/sessions/${encodeURIComponent(targetSessionID)}`,
                      location.pathname,
                    ),
                    {
                      state:
                        turn.command && !turn.turn_id
                          ? { commandInput: text, command: turn.command }
                          : turn.turn_id
                            ? { activeTurnID: turn.turn_id }
                            : undefined,
                    },
                  );
                } catch (e) {
                  const message =
                    e instanceof Error ? e.message : "Failed to start chat.";
                  setError(message);
                  throw e;
                } finally {
                  setSending(false);
                }
              }}
            >
              <PromptInputTextarea placeholder="Ask juex anything..." />
              <PromptInputFooter className="justify-end">
                <PromptInputSubmit disabled={sending} status={sending ? "submitted" : undefined} />
              </PromptInputFooter>
            </PromptInput>
          )}
          {error ? (
            <div
              role="alert"
              className="mt-2 text-left text-xs text-destructive"
            >
              {error}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
