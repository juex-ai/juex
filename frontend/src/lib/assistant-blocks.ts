import type {
  Block,
  ImageBlock,
  MediaRef,
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
      const timeoutSeconds = numberValue(call.timeout_seconds);
      if (timeoutSeconds !== undefined) block.timeout_seconds = timeoutSeconds;
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
    case "image":
      return [imageBlockFromRecord(value)];
    case "tool_use":
      return [toolUseBlockFromRecord(value)];
    case "tool_result":
      return [toolResultBlockFromRecord(value)];
    default:
      return [];
  }
}

function imageBlockFromRecord(value: Record<string, unknown>): ImageBlock {
  const block: ImageBlock = { type: "image" };
  if (isRecord(value.media)) block.media = mediaRefFromRecord(value.media);
  return block;
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
  const timeoutSeconds = numberValue(value.timeout_seconds);
  if (timeoutSeconds !== undefined) block.timeout_seconds = timeoutSeconds;
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
  if (isRecord(value.media)) {
    block.media = mediaRefFromRecord(value.media);
  }
  return block;
}

function mediaRefFromRecord(value: Record<string, unknown>): MediaRef {
  const media: MediaRef = {};
  if (typeof value.artifact_path === "string") media.artifact_path = value.artifact_path;
  if (typeof value.media_type === "string") media.media_type = value.media_type;
  if (typeof value.sha256 === "string") media.sha256 = value.sha256;
  const originalBytes = numberValue(value.original_bytes);
  if (originalBytes !== undefined) media.original_bytes = originalBytes;
  const width = numberValue(value.width);
  if (width !== undefined) media.width = width;
  const height = numberValue(value.height);
  if (height !== undefined) media.height = height;
  return media;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}
