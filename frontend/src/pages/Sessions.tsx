import { LogoMark } from "@/components/LogoMark";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { createSession, startTurn } from "@/api";
import {
  PromptInput,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
} from "@/components/ai-elements/prompt-input";
import { useShellTitle } from "@/components/AppShell";

export function Sessions() {
  const navigate = useNavigate();
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useShellTitle(null);

  return (
    <div className="flex flex-1 items-center justify-center px-4 py-8 text-muted-foreground sm:p-8">
      <div className="flex w-full max-w-[760px] flex-col items-center text-center">
        <LogoMark className="mb-4 size-14 text-primary" />
        <p className="font-serif text-2xl italic leading-tight text-primary sm:text-3xl">
          Aware, action
        </p>
        <div className="mt-6 w-full">
          <PromptInput
            onSubmit={async (msg) => {
              const text = msg.text?.trim();
              if (!text) return;
              setSending(true);
              setError(null);
              try {
                const session = await createSession();
                const turn = await startTurn(session.id, text);
                navigate(`/sessions/${encodeURIComponent(session.id)}`, {
                  state: turn.command
                    ? { commandInput: text, command: turn.command }
                    : undefined,
                });
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
          {error ? (
            <div className="mt-2 text-left text-xs text-destructive">{error}</div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
