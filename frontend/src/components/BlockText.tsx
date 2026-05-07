import type { TextBlock } from "@/types";
import { Markdown } from "@/components/prompt-kit/markdown";

export function BlockText({ block }: { block: TextBlock }) {
  return (
    <div className="prose prose-sm max-w-none dark:prose-invert">
      <Markdown>{block.text}</Markdown>
    </div>
  );
}
