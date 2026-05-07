import type { ToolResultBlock } from "@/types";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

const PREVIEW_MAX = 120;

export function BlockToolResult({ block }: { block: ToolResultBlock }) {
  const preview = makePreview(block.content, PREVIEW_MAX);
  const isError = !!block.is_error;
  return (
    <Collapsible
      className={cn("rounded-md border", isError && "border-juex-error/40")}
    >
      <CollapsibleTrigger className="group flex w-full items-center gap-2 px-3 py-2 text-left text-xs">
        <ChevronRight className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        <span
          className={cn(
            "truncate",
            isError ? "text-juex-error" : "text-muted-foreground",
          )}
        >
          {isError ? "[error] " : ""}
          {preview}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <pre className="bg-muted/40 max-h-[28rem] overflow-y-auto p-3 text-xs whitespace-pre-wrap">
          <code>{block.content}</code>
        </pre>
      </CollapsibleContent>
    </Collapsible>
  );
}

function makePreview(s: string, n: number): string {
  if (!s) return "(empty)";
  const collapsed = s.replace(/\s+/g, " ").trim();
  return collapsed.length <= n ? collapsed : collapsed.slice(0, n) + "...";
}
