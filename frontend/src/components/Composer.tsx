import { useState, type KeyboardEvent } from "react";
import { Send, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { StatusPill, type Status } from "./StatusPill";

export interface ComposerProps {
  status: Status;
  onSend: (text: string) => void;
  onInterrupt: () => void;
}

export function Composer({ status, onSend, onInterrupt }: ComposerProps) {
  const [text, setText] = useState("");
  const busy = status.kind === "running" || status.kind === "tool";

  function submit() {
    const t = text.trim();
    if (!t) return;
    onSend(t);
    setText("");
  }

  function handleKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    } else if (e.key === "Escape") {
      e.currentTarget.blur();
    }
  }

  return (
    <footer className="bg-background/95 sticky bottom-0 border-t p-4 backdrop-blur">
      <Textarea
        placeholder="Type a prompt..."
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKey}
        rows={3}
        className="min-h-[3rem] resize-none"
      />
      <div className="mt-2 flex items-center justify-between">
        <StatusPill status={status} />
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={onInterrupt}
            disabled={!busy}
          >
            <Square className="mr-2 size-3.5" />
            Stop
          </Button>
          <Button onClick={submit} disabled={!text.trim() || busy}>
            <Send className="mr-2 size-3.5" />
            Send
          </Button>
        </div>
      </div>
    </footer>
  );
}
