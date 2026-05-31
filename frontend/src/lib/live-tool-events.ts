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
  let changed = false;
  const next = messages.map((message) => {
    if (message.turn_id !== update.turnID || message.role !== "assistant") {
      return message;
    }
    const blocks = message.blocks ?? [];
    const existingIndex = blocks.findIndex(
      (candidate) =>
        candidate.type === "tool_use" &&
        candidate.tool_use_id === update.toolUseID,
    );
    if (existingIndex >= 0) {
      changed = true;
      return {
        ...message,
        blocks: blocks.map((candidate, index) =>
          index === existingIndex ? { ...candidate, ...block } : candidate,
        ),
      };
    }
    if (message.pending) {
      changed = true;
      return { ...message, blocks: [...blocks, block] };
    }
    return message;
  });
  if (changed) return next;
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
