import type {
  Block,
  ImageBlock,
  Message,
  ReasoningBlock,
  Role,
  RuntimeToolCallState,
  RuntimeToolCallStatus,
  TextBlock,
  ToolResultBlock,
  ToolUseBlock,
} from "@/types";
import type { ToolUIPartState } from "@/components/ai-elements/_local-types";

export type ToolDisplayUnit = {
  kind: "tool";
  use: ToolUseBlock | null;
  result: ToolResultBlock | null;
  state?: RuntimeToolCallState;
};

export type ToolBatchDisplayUnit = {
  kind: "tool_batch";
  tools: ToolDisplayUnit[];
};

export type DisplayUnit =
  | { kind: "text"; block: TextBlock }
  | { kind: "image"; block: ImageBlock }
  | { kind: "reasoning"; block: ReasoningBlock }
  | ToolDisplayUnit
  | ToolBatchDisplayUnit;

type UnbatchedDisplayUnit = Exclude<DisplayUnit, ToolBatchDisplayUnit>;

export type MessageGroup = {
  key: string;
  id?: string;
  createdAt?: string;
  role: Role;
  kind?: string;
  pending: boolean;
  units: DisplayUnit[];
  /** Model that produced this message (assistant only). */
  model?: string;
};

export type MessageGroupingOptions = {
  runtimeStatusLoaded?: boolean;
  activeTurnID?: string;
};

function normalizeTextBlock(block: TextBlock): TextBlock {
  // Older transcripts can contain {"type":"text"} for empty provider output.
  if (typeof block.text === "string") return block;
  return { ...block, text: "" };
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
  toolStatuses: readonly RuntimeToolCallStatus[] = [],
  options: MessageGroupingOptions = {},
): MessageGroup[] {
  if (!messages?.length) return [];
  const groups: MessageGroup[] = [];
  const toolById = new Map<string, ToolDisplayUnit>();
  const toolTurnById = new Map<string, string | undefined>();
  const toolStateByID = new Map(
    toolStatuses.map((tool) => [tool.tool_use_id, tool.state]),
  );

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    const units: UnbatchedDisplayUnit[] = [];
    for (const block of msg.blocks ?? []) {
      switch (block.type) {
        case "text":
          units.push({ kind: "text", block: normalizeTextBlock(block) });
          break;
        case "image":
          units.push({ kind: "image", block });
          break;
        case "reasoning":
          units.push({ kind: "reasoning", block });
          break;
        case "tool_use": {
          const unit = {
            kind: "tool" as const,
            use: block,
            result: null,
            state: toolStateByID.get(block.tool_use_id),
          };
          units.push(unit);
          if (block.tool_use_id) {
            toolById.set(block.tool_use_id, unit);
            toolTurnById.set(block.tool_use_id, msg.turn_id);
          }
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
            units.push({
              kind: "tool",
              use: null,
              result: block,
              state: block.tool_use_id
                ? toolStateByID.get(block.tool_use_id)
                : undefined,
            });
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
      createdAt: msg.created_at,
      role: msg.role,
      kind: msg.kind,
      pending,
      units: foldToolBatches(units),
      model: msg.model,
    });
  }

  if (options.runtimeStatusLoaded) {
    for (const [toolUseID, unit] of toolById) {
      if (
        unit.state === undefined &&
        unit.result === null &&
        (!options.activeTurnID ||
          toolTurnById.get(toolUseID) !== options.activeTurnID)
      ) {
        unit.state = "errored";
      }
    }
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
  runtimeState?: RuntimeToolCallState,
): ToolUIPartState {
  switch (runtimeState) {
    case "requested":
      return "input-streaming";
    case "running":
    case "streaming":
      return "input-available";
    case "completed":
      return "output-available";
    case "errored":
      return "output-error";
  }
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
