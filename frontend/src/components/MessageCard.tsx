import type { Message } from "@/types";
import { cn } from "@/lib/utils";
import { BlockText } from "./BlockText";
import { BlockThinking } from "./BlockThinking";
import { BlockToolUse } from "./BlockToolUse";
import { BlockToolResult } from "./BlockToolResult";
import { Loader2 } from "lucide-react";

const roleBorder: Record<string, string> = {
  user: "border-l-juex-user",
  assistant: "border-l-juex-assistant",
  system: "border-l-juex-thinking",
};
const roleText: Record<string, string> = {
  user: "text-juex-user",
  assistant: "text-juex-assistant",
  system: "text-juex-thinking",
};

export function MessageCard({
  message,
  model,
}: {
  message: Message;
  model?: string;
}) {
  const role = message.role;
  const blocks = message.blocks ?? [];
  return (
    <article
      className={cn(
        "bg-card rounded-lg border border-l-2 px-3 py-2.5 shadow-sm",
        roleBorder[role] ?? "border-l-muted",
      )}
    >
      <header className="mb-1 flex items-baseline gap-2">
        <span
          className={cn(
            "text-xs font-semibold tracking-wide uppercase",
            roleText[role] ?? "text-muted-foreground",
          )}
        >
          {role}
        </span>
        {role === "assistant" && model ? (
          <span className="text-muted-foreground text-[11px] font-medium">
            ({model})
          </span>
        ) : null}
      </header>
      <div className="space-y-2">
        {message.pending && blocks.length === 0 ? (
          <div className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2 className="size-4 animate-spin" />
            Waiting for assistant...
          </div>
        ) : null}
        {blocks.map((block, i) => {
          switch (block.type) {
            case "text":
              return <BlockText key={i} block={block} />;
            case "reasoning":
              return <BlockThinking key={i} block={block} />;
            case "tool_use":
              return <BlockToolUse key={i} block={block} />;
            case "tool_result":
              return <BlockToolResult key={i} block={block} />;
            default:
              return null;
          }
        })}
      </div>
    </article>
  );
}
