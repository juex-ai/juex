import type { Message, ToolUseBlock } from "../types";

export type ToolRequestedUpdate = {
  turnID: string | undefined;
  toolUseID: string | undefined;
  toolName: string;
  input?: Record<string, unknown>;
  timeoutSeconds?: number;
};

export function applyToolRequestedToMessages(
  messages: Message[],
  update: ToolRequestedUpdate,
): Message[] {
  if (!update.turnID || !update.toolUseID) return messages;
  const block = toolUseBlockFromUpdate(update);
  const targetIndex = toolUpdateTargetIndex(messages, update);
  if (targetIndex >= 0) {
    return messages.map((message, index) => {
      if (index !== targetIndex) {
        return message;
      }
      const blocks = message.blocks ?? [];
      const existingIndex = blocks.findIndex(
        (candidate) =>
          candidate.type === "tool_use" &&
          candidate.tool_use_id === update.toolUseID,
      );
      if (existingIndex >= 0) {
        return {
          ...message,
          blocks: blocks.map((candidate, blockIndex) =>
            blockIndex === existingIndex ? { ...candidate, ...block } : candidate,
          ),
        };
      }
      return { ...message, blocks: [...blocks, block] };
    });
  }
  return [
    ...messages,
    {
      role: "assistant",
      turn_id: update.turnID,
      pending: true,
      blocks: [block],
    },
  ];
}

function toolUpdateTargetIndex(
  messages: Message[],
  update: ToolRequestedUpdate,
): number {
  let sameTurnAssistant = -1;
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index];
    if (message.turn_id !== update.turnID || message.role !== "assistant") {
      continue;
    }
    const blocks = message.blocks ?? [];
    if (sameTurnAssistant < 0) {
      sameTurnAssistant = index;
    }
    if (
      blocks.some(
        (candidate) =>
          candidate.type === "tool_use" &&
          candidate.tool_use_id === update.toolUseID,
      )
    ) {
      return index;
    }
  }
  return sameTurnAssistant;
}

function toolUseBlockFromUpdate(update: ToolRequestedUpdate): ToolUseBlock {
  const block: ToolUseBlock = {
    type: "tool_use",
    tool_use_id: update.toolUseID ?? "",
    tool_name: update.toolName,
  };
  if (update.input) block.input = update.input;
  if (
    Number.isFinite(update.timeoutSeconds) &&
    update.timeoutSeconds !== undefined &&
    update.timeoutSeconds > 0
  ) {
    block.timeout_seconds = update.timeoutSeconds;
  }
  return block;
}
