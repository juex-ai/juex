import { LogoMark } from "@/components/LogoMark";
import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { createSession, getActiveSession, startTurn } from "@/api";
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

export function Sessions() {
  const navigate = useNavigate();
  const location = useLocation();
  const { agent, agentsLoaded } = useFleetAgent();
  const [checkingSession, setCheckingSession] = useState(true);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [sending, setSending] = useState(false);
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  useShellTitle(null);

  useEffect(() => {
    let live = true;
    setCheckingSession(true);
    setLookupError(null);
    getActiveSession()
      .then(({ session_id }) => {
        if (!live) return;
        const href = homeActiveSessionHref(session_id, location.pathname);
        if (href) {
          navigate(href, { replace: true });
        }
      })
      .catch((e) => {
        if (!live) return;
        console.error("getActiveSession failed", e);
        setLookupError(
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

  if (lookupError) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center px-4 py-8">
        <div className="flex max-w-md flex-col items-center gap-3 text-center">
          <div
            role="alert"
            className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {lookupError}
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

  if (checkingSession) {
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
                setSubmitError(null);
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
                          : undefined,
                    },
                  );
                } catch (e) {
                  const message =
                    e instanceof Error ? e.message : "Failed to start chat.";
                  setSubmitError(message);
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
          {submitError ? (
            <div
              role="alert"
              className="mt-2 text-left text-xs text-destructive"
            >
              {submitError}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
