import type {
  Block,
  ReasoningBlock,
  ToolResultBlock,
  ToolUseBlock,
} from "../types";

export function assistantBlocksFromEventPayload(payload: unknown): Block[] {
  if (!isRecord(payload)) return [];

  if (Array.isArray(payload.blocks)) {
    return payload.blocks.flatMap(blockFromUnknown);
  }

  const blocks: Block[] = [];
  if (typeof payload.thinking === "string" && payload.thinking) {
    blocks.push({ type: "reasoning", text: payload.thinking });
  }
  if (typeof payload.text === "string" && payload.text) {
    blocks.push({ type: "text", text: payload.text });
  }
  if (Array.isArray(payload.tool_calls)) {
    for (const call of payload.tool_calls) {
      if (!isRecord(call)) continue;
      const block: ToolUseBlock = {
        type: "tool_use",
        tool_use_id:
          typeof call.tool_use_id === "string" ? call.tool_use_id : "",
        tool_name: typeof call.name === "string" ? call.name : "?",
      };
      if (isRecord(call.input)) block.input = call.input;
      blocks.push(block);
    }
  }
  return blocks;
}

function blockFromUnknown(value: unknown): Block[] {
  if (!isRecord(value) || typeof value.type !== "string") return [];

  switch (value.type) {
    case "text":
      return typeof value.text === "string"
        ? [{ type: "text", text: value.text }]
        : [];
    case "reasoning":
      return [reasoningBlockFromRecord(value)];
    case "tool_use":
      return [toolUseBlockFromRecord(value)];
    case "tool_result":
      return [toolResultBlockFromRecord(value)];
    default:
      return [];
  }
}

function reasoningBlockFromRecord(value: Record<string, unknown>): ReasoningBlock {
  const block: ReasoningBlock = { type: "reasoning" };
  if (typeof value.text === "string") block.text = value.text;
  if (typeof value.content === "string") block.content = value.content;
  if (typeof value.signature === "string") block.signature = value.signature;
  if (typeof value.redacted === "boolean") block.redacted = value.redacted;
  return block;
}

function toolUseBlockFromRecord(value: Record<string, unknown>): ToolUseBlock {
  const block: ToolUseBlock = {
    type: "tool_use",
    tool_use_id:
      typeof value.tool_use_id === "string" ? value.tool_use_id : "",
    tool_name:
      typeof value.tool_name === "string"
        ? value.tool_name
        : typeof value.name === "string"
          ? value.name
          : "?",
  };
  if (isRecord(value.input)) block.input = value.input;
  return block;
}

function toolResultBlockFromRecord(
  value: Record<string, unknown>,
): ToolResultBlock {
  const block: ToolResultBlock = {
    type: "tool_result",
    content: typeof value.content === "string" ? value.content : "",
  };
  if (typeof value.tool_use_id === "string") {
    block.tool_use_id = value.tool_use_id;
  }
  if (typeof value.is_error === "boolean") {
    block.is_error = value.is_error;
  }
  return block;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
