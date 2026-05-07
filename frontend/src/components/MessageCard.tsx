import type { Message } from "@/types";
import { cn } from "@/lib/utils";
import { BlockText } from "./BlockText";
import { BlockThinking } from "./BlockThinking";
import { BlockToolUse } from "./BlockToolUse";
import { BlockToolResult } from "./BlockToolResult";

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

export function MessageCard({ message }: { message: Message }) {
  const role = message.role;
  return (
    <article
      className={cn(
        "bg-card rounded-lg border border-l-2 p-4 shadow-sm",
        roleBorder[role] ?? "border-l-muted",
      )}
    >
      <header className="mb-2">
        <span
          className={cn(
            "text-xs font-semibold tracking-wide uppercase",
            roleText[role] ?? "text-muted-foreground",
          )}
        >
          {role}
        </span>
      </header>
      <div className="space-y-3">
        {message.blocks.map((block, i) => {
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
