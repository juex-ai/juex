import type { ToolUseBlock } from "@/types";
import { Wrench } from "lucide-react";

export function BlockToolUse({ block }: { block: ToolUseBlock }) {
  const json = JSON.stringify(block.input ?? {}, null, 2);
  return (
    <aside className="bg-juex-tool-bg border-l-juex-tool rounded-md border border-l-2 p-3">
      <header className="mb-2 flex items-baseline gap-2">
        <Wrench className="text-juex-tool size-4" />
        <strong className="text-juex-tool font-semibold">
          {block.tool_name}
        </strong>
        <span className="text-muted-foreground font-mono text-xs">
          #{block.tool_use_id.slice(0, 8)}
        </span>
      </header>
      <pre className="bg-background overflow-x-auto rounded p-2 text-xs">
        <code>{json}</code>
      </pre>
    </aside>
  );
}
