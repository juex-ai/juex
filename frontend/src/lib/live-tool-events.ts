import type { Message, ToolResultBlock, ToolUseBlock } from "../types";

const LIVE_TOOL_OUTPUT_MAX_CHARS = 12000;

export type ToolRequestedUpdate = {
  turnID: string | undefined;
  toolUseID: string | undefined;
  toolName: string;
  input?: Record<string, unknown>;
  timeoutSeconds?: number;
};

export type ToolOutputDeltaUpdate = {
  turnID: string | undefined;
  toolUseID: string | undefined;
  toolName?: string;
  text: string | undefined;
};

export type ToolResultUpdate = {
  turnID: string | undefined;
  toolUseID: string | undefined;
  toolName?: string;
  content: string | undefined;
  isError?: boolean;
  timeoutSeconds?: number;
};

type ToolUsePlaceholderUpdate = {
  turnID: string | undefined;
  toolUseID: string | undefined;
  toolName?: string;
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

export function applyToolOutputDeltaToMessages(
  messages: Message[],
  update: ToolOutputDeltaUpdate,
): Message[] {
  if (!update.turnID || !update.toolUseID || !update.text) return messages;
  messages = ensureToolUseBeforeResult(messages, update);
  const block: ToolResultBlock = {
    type: "tool_result",
    tool_use_id: update.toolUseID,
    content: update.text,
  };
  return upsertToolResultBlock(messages, update, block, "append");
}

export function applyToolResultToMessages(
  messages: Message[],
  update: ToolResultUpdate,
): Message[] {
  if (!update.turnID || !update.toolUseID) return messages;
  messages = ensureToolUseBeforeResult(messages, update);
  const block: ToolResultBlock = {
    type: "tool_result",
    tool_use_id: update.toolUseID,
    content: update.content ?? "",
  };
  if (update.isError) block.is_error = true;
  return upsertToolResultBlock(messages, update, block, "final");
}

function upsertToolResultBlock(
  messages: Message[],
  update: Pick<ToolResultUpdate, "turnID" | "toolUseID">,
  block: ToolResultBlock,
  mode: "append" | "final",
): Message[] {
  const targetIndex = toolResultTargetIndex(messages, update);
  if (targetIndex >= 0) {
    return messages.map((message, index) => {
      if (index !== targetIndex) return message;
      const blocks = message.blocks ?? [];
      const existingIndex = blocks.findIndex(
        (candidate) =>
          candidate.type === "tool_result" &&
          candidate.tool_use_id === update.toolUseID,
      );
      if (existingIndex >= 0) {
        return {
          ...message,
          blocks: blocks.map((candidate, blockIndex) => {
            if (blockIndex !== existingIndex || candidate.type !== "tool_result") {
              return candidate;
            }
            return mergeToolResultBlock(candidate, block, mode);
          }),
        };
      }
      return { ...message, blocks: [...blocks, block] };
    });
  }
  return [
    ...messages,
    {
      role: "user",
      turn_id: update.turnID,
      blocks: [block],
    },
  ];
}

function ensureToolUseBeforeResult(
  messages: Message[],
  update: ToolUsePlaceholderUpdate,
): Message[] {
  if (!update.turnID || !update.toolUseID || !update.toolName) return messages;

  const firstResultIndex = messages.findIndex(
    (message) =>
      message.turn_id === update.turnID &&
      message.role === "user" &&
      message.blocks?.some(
        (candidate) =>
          candidate.type === "tool_result" &&
          candidate.tool_use_id === update.toolUseID,
      ),
  );
  let assistantBeforeResult = -1;
  let matchingAssistantBeforeResult = -1;
  for (let index = 0; index < messages.length; index++) {
    if (firstResultIndex >= 0 && index >= firstResultIndex) break;
    const message = messages[index];
    if (message.turn_id !== update.turnID || message.role !== "assistant") {
      continue;
    }
    assistantBeforeResult = index;
    const hasMatchingToolUse = message.blocks?.some(
      (candidate) =>
        candidate.type === "tool_use" &&
        candidate.tool_use_id === update.toolUseID,
    );
    if (hasMatchingToolUse) matchingAssistantBeforeResult = index;
  }

  if (matchingAssistantBeforeResult >= 0) {
    const block = toolUseBlockFromUpdate({
      turnID: update.turnID,
      toolUseID: update.toolUseID,
      toolName: update.toolName,
      timeoutSeconds: update.timeoutSeconds,
    });
    return messages.map((message, index) => {
      if (index !== matchingAssistantBeforeResult) return message;
      return {
        ...message,
        blocks: (message.blocks ?? []).map((candidate) =>
          candidate.type === "tool_use" &&
          candidate.tool_use_id === update.toolUseID
            ? { ...candidate, ...block }
            : candidate,
        ),
      };
    });
  }

  const block = toolUseBlockFromUpdate({
    turnID: update.turnID,
    toolUseID: update.toolUseID,
    toolName: update.toolName,
    timeoutSeconds: update.timeoutSeconds,
  });
  if (assistantBeforeResult >= 0) {
    return messages.map((message, index) =>
      index === assistantBeforeResult
        ? { ...message, blocks: [...(message.blocks ?? []), block] }
        : message,
    );
  }

  const insertAt = firstResultIndex >= 0 ? firstResultIndex : messages.length;
  return [
    ...messages.slice(0, insertAt),
    {
      role: "assistant",
      turn_id: update.turnID,
      pending: true,
      blocks: [block],
    },
    ...messages.slice(insertAt),
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

function toolResultTargetIndex(
  messages: Message[],
  update: Pick<ToolResultUpdate, "turnID" | "toolUseID">,
): number {
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index];
    if (message.turn_id !== update.turnID || message.role !== "user") {
      continue;
    }
    const blocks = message.blocks ?? [];
    if (blocks.every((candidate) => candidate.type === "tool_result")) {
      return index;
    }
  }
  return -1;
}

function mergeToolResultBlock(
  current: ToolResultBlock,
  incoming: ToolResultBlock,
  mode: "append" | "final",
): ToolResultBlock {
  if (mode === "append") {
    return {
      ...current,
      content: capLiveToolOutput(current.content + incoming.content),
    };
  }
  if (incoming.is_error) {
    return { ...current, content: incoming.content, is_error: true };
  }
  const next = {
    ...current,
    content: current.content || incoming.content,
  };
  delete next.is_error;
  return next;
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

function capLiveToolOutput(text: string): string {
  let previousOmitted = 0;
  const match = text.match(
    /^\[live output truncated: (\d+) earlier characters omitted\]\n/,
  );
  if (match) {
    previousOmitted = Number.parseInt(match[1], 10);
    text = text.slice(match[0].length);
  }
  if (text.length <= LIVE_TOOL_OUTPUT_MAX_CHARS) {
    if (previousOmitted > 0) {
      return `[live output truncated: ${previousOmitted} earlier characters omitted]\n${text}`;
    }
    return text;
  }
  const omitted = previousOmitted + text.length - LIVE_TOOL_OUTPUT_MAX_CHARS;
  return `[live output truncated: ${omitted} earlier characters omitted]\n${text.slice(
    -LIVE_TOOL_OUTPUT_MAX_CHARS,
  )}`;
}
