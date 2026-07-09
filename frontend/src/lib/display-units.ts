import type {
  Block,
  ImageBlock,
  Message,
  ReasoningBlock,
  Role,
  TextBlock,
  ToolResultBlock,
  ToolUseBlock,
} from "@/types";
import type { ToolUIPartState } from "@/components/ai-elements/_local-types";

export type ToolDisplayUnit = {
  kind: "tool";
  use: ToolUseBlock | null;
  result: ToolResultBlock | null;
};

export type ToolBatchDisplayUnit = {
  kind: "tool_batch";
  tools: ToolDisplayUnit[];
};

export type DisplayUnit =
  | { kind: "text"; block: TextBlock }
  | { kind: "reasoning"; block: ReasoningBlock }
  | ToolDisplayUnit
  | ToolBatchDisplayUnit;

type UnbatchedDisplayUnit = Exclude<DisplayUnit, ToolBatchDisplayUnit>;

export type MessageGroup = {
  key: string;
  id?: string;
  role: Role;
  kind?: string;
  pending: boolean;
  units: DisplayUnit[];
  /** Model that produced this message (assistant only). */
  model?: string;
};

function normalizeTextBlock(block: TextBlock): TextBlock {
  // Older transcripts can contain {"type":"text"} for empty provider output.
  if (typeof block.text === "string") return block;
  return { ...block, text: "" };
}

function imageReferenceText(block: ImageBlock): string {
  const media = block.media;
  if (!media) return "[image: missing media reference]";
  const parts: string[] = [];
  if (media.artifact_path) parts.push(`path=${media.artifact_path}`);
  if (media.media_type) parts.push(`type=${media.media_type}`);
  if (media.sha256) parts.push(`sha256=${media.sha256}`);
  if (media.original_bytes) parts.push(`bytes=${media.original_bytes}`);
  if (media.width && media.height) parts.push(`size=${media.width}x${media.height}`);
  return parts.length > 0
    ? `[image: ${parts.join(" ")}]`
    : "[image: empty media reference]";
}

// Walk all messages in order and produce the render groups. tool_use lives in
// an assistant message and the matching tool_result lives in the next user
// message (Anthropic-style); we fold them into one tool unit on the assistant
// group so the UI shows a single Tool card instead of two bubbles.
//
// A user message whose blocks were all paired upstream produces no group —
// the bubble is suppressed entirely. Orphan tool_results (no matching id)
// stay where they appear, as standalone output-only Tool cards.
export function messagesToGroups(
  messages: Message[] | null | undefined,
): MessageGroup[] {
  if (!messages?.length) return [];
  const groups: MessageGroup[] = [];
  const toolById = new Map<string, ToolDisplayUnit>();

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    const units: UnbatchedDisplayUnit[] = [];
    for (const block of msg.blocks ?? []) {
      switch (block.type) {
        case "text":
          units.push({ kind: "text", block: normalizeTextBlock(block) });
          break;
        case "image":
          units.push({
            kind: "text",
            block: { type: "text", text: imageReferenceText(block) },
          });
          break;
        case "reasoning":
          units.push({ kind: "reasoning", block });
          break;
        case "tool_use": {
          const unit = { kind: "tool" as const, use: block, result: null };
          units.push(unit);
          if (block.tool_use_id) toolById.set(block.tool_use_id, unit);
          break;
        }
        case "tool_result": {
          const existing = block.tool_use_id
            ? toolById.get(block.tool_use_id)
            : undefined;
          if (existing) {
            // Merge into the earlier assistant group; do not emit a unit on
            // this user message. Last-wins on repeated ids (the runtime emits
            // exactly one result per use today).
            existing.result = block;
          } else {
            units.push({ kind: "tool", use: null, result: block });
          }
          break;
        }
      }
    }
    const pending = Boolean(msg.pending);
    // Drop messages that contributed nothing of their own AND are not pending.
    // A user message whose only blocks were tool_results paired upstream lands
    // here and is silently suppressed.
    if (units.length === 0 && !pending) continue;
    groups.push({
      key: msg.id ?? `${msg.turn_id ?? "msg"}-${i}`,
      id: msg.id,
      role: msg.role,
      kind: msg.kind,
      pending,
      units: foldToolBatches(units),
      model: msg.model,
    });
  }

  return groups;
}

function foldToolBatches(units: UnbatchedDisplayUnit[]): DisplayUnit[] {
  const folded: DisplayUnit[] = [];
  let batch: ToolDisplayUnit[] = [];

  const flushBatch = () => {
    if (batch.length === 1) {
      folded.push(batch[0]);
    } else if (batch.length > 1) {
      folded.push({ kind: "tool_batch", tools: batch });
    }
    batch = [];
  };

  for (const unit of units) {
    if (unit.kind === "tool") {
      batch.push(unit);
      continue;
    }
    flushBatch();
    folded.push(unit);
  }
  flushBatch();

  return folded;
}

export function toolState(
  use: ToolUseBlock | null,
  result: ToolResultBlock | null,
): ToolUIPartState {
  if (result?.is_error) return "output-error";
  if (result?.streaming) return "input-available";
  if (result) return "output-available";
  if (use) return "input-available";
  // Should not happen — a tool unit always has at least one side.
  return "input-available";
}

// Kept as a thin wrapper for callers that still want per-message folding (e.g.
// preview tooling or future single-message viewers). Prefer `messagesToGroups`
// at the Session render layer because tool pairs cross message boundaries.
export function toDisplayUnits(
  blocks: Block[] | null | undefined,
): DisplayUnit[] {
  if (!blocks?.length) return [];
  return messagesToGroups([{ role: "assistant", blocks }])[0]?.units ?? [];
}
