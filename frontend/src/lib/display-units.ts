import type {
  Block,
  ReasoningBlock,
  TextBlock,
  ToolResultBlock,
  ToolUseBlock,
} from "@/types";
import type { ToolUIPartState } from "@/components/ai-elements/_local-types";

export type DisplayUnit =
  | { kind: "text"; block: TextBlock }
  | { kind: "reasoning"; block: ReasoningBlock }
  | {
      kind: "tool";
      use: ToolUseBlock | null;
      result: ToolResultBlock | null;
    };

// Fold the persistent Block[] stream into DisplayUnit[]:
// - text / reasoning blocks pass through in order
// - a tool_use emits a tool unit at its position, remembered by tool_use_id
// - a tool_result with a matching id attaches to its tool unit
// - a tool_result with no match (or no id) emits an orphan tool unit
export function toDisplayUnits(blocks: Block[] | null | undefined): DisplayUnit[] {
  if (!blocks?.length) return [];
  const units: DisplayUnit[] = [];
  const byId = new Map<string, Extract<DisplayUnit, { kind: "tool" }>>();
  for (const block of blocks) {
    switch (block.type) {
      case "text":
        units.push({ kind: "text", block });
        break;
      case "reasoning":
        units.push({ kind: "reasoning", block });
        break;
      case "tool_use": {
        const unit = { kind: "tool" as const, use: block, result: null };
        units.push(unit);
        if (block.tool_use_id) byId.set(block.tool_use_id, unit);
        break;
      }
      case "tool_result": {
        const existing = block.tool_use_id ? byId.get(block.tool_use_id) : undefined;
        if (existing) {
          existing.result = block;
        } else {
          units.push({ kind: "tool", use: null, result: block });
        }
        break;
      }
    }
  }
  return units;
}

export function toolState(
  use: ToolUseBlock | null,
  result: ToolResultBlock | null,
): ToolUIPartState {
  if (result?.is_error) return "output-error";
  if (result) return "output-available";
  if (use) return "input-available";
  // Should not happen — a tool unit always has at least one side.
  return "input-available";
}
