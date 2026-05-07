import type { ReasoningBlock } from "@/types";
import {
  Reasoning,
  ReasoningTrigger,
  ReasoningContent,
} from "@/components/prompt-kit/reasoning";

export function BlockThinking({ block }: { block: ReasoningBlock }) {
  const text = block.redacted
    ? "[redacted by provider]"
    : block.text ?? block.content ?? "";

  return (
    <Reasoning>
      <ReasoningTrigger className="text-muted-foreground text-xs">
        Thinking{block.redacted ? " [redacted]" : "..."}
      </ReasoningTrigger>
      <ReasoningContent>{text}</ReasoningContent>
    </Reasoning>
  );
}
