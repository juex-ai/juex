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

type ToolResultTarget = {
  messageIndex: number;
  insertIndex: number;
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
  const target = toolResultTarget(messages, update);
  if (target.messageIndex >= 0) {
    return messages.map((message, index) => {
      if (index !== target.messageIndex) return message;
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
  const insertAt = Math.min(Math.max(target.insertIndex, 0), messages.length);
  const message: Message = {
    role: "user",
    turn_id: update.turnID,
    blocks: [block],
  };
  return [
    ...messages.slice(0, insertAt),
    message,
    ...messages.slice(insertAt),
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
  let matchingToolUse: ToolUseBlock | undefined;
  for (let index = 0; index < messages.length; index++) {
    if (firstResultIndex >= 0 && index >= firstResultIndex) break;
    const message = messages[index];
    if (message.turn_id !== update.turnID || message.role !== "assistant") {
      continue;
    }
    assistantBeforeResult = index;
    const existingToolUse = message.blocks?.find(
      (candidate) =>
        candidate.type === "tool_use" &&
        candidate.tool_use_id === update.toolUseID,
    );
    if (existingToolUse?.type === "tool_use") {
      matchingAssistantBeforeResult = index;
      matchingToolUse = existingToolUse;
    }
  }

  if (matchingAssistantBeforeResult >= 0) {
    if (!toolUseNeedsMetadataUpdate(matchingToolUse, update)) return messages;
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

function toolUseNeedsMetadataUpdate(
  current: ToolUseBlock | undefined,
  update: ToolUsePlaceholderUpdate,
): boolean {
  if (!current) return true;
  if (update.toolName && current.tool_name !== update.toolName) return true;
  return (
    Number.isFinite(update.timeoutSeconds) &&
    update.timeoutSeconds !== undefined &&
    update.timeoutSeconds > 0 &&
    current.timeout_seconds !== update.timeoutSeconds
  );
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

function toolResultTarget(
  messages: Message[],
  update: Pick<ToolResultUpdate, "turnID" | "toolUseID">,
): ToolResultTarget {
  const fallback: ToolResultTarget = {
    messageIndex: -1,
    insertIndex: messages.length,
  };
  let matchingToolUseIndex = -1;
  let seenTurn = false;
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index];
    if (message.turn_id !== update.turnID) {
      if (seenTurn) {
        break;
      }
      continue;
    }
    seenTurn = true;
    const blocks = message.blocks ?? [];
    if (
      message.role === "user" &&
      blocks.every((candidate) => candidate.type === "tool_result")
    ) {
      if (
        blocks.some(
          (candidate) =>
            candidate.type === "tool_result" &&
            candidate.tool_use_id === update.toolUseID,
        )
      ) {
        return { messageIndex: index, insertIndex: -1 };
      }
      if (fallback.messageIndex < 0) {
        fallback.messageIndex = index;
      }
    }
    if (
      matchingToolUseIndex < 0 &&
      message.role === "assistant" &&
      blocks.some(
        (candidate) =>
          candidate.type === "tool_use" &&
          candidate.tool_use_id === update.toolUseID,
      )
    ) {
      matchingToolUseIndex = index;
    }
  }
  if (matchingToolUseIndex < 0) {
    return fallback;
  }
  for (let index = matchingToolUseIndex + 1; index < messages.length; index++) {
    const message = messages[index];
    if (message.turn_id !== update.turnID) break;
    if (message.role === "assistant") break;
    const blocks = message.blocks ?? [];
    if (
      message.role === "user" &&
      blocks.every((candidate) => candidate.type === "tool_result")
    ) {
      return { messageIndex: index, insertIndex: -1 };
    }
  }
  return { messageIndex: -1, insertIndex: matchingToolUseIndex + 1 };
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
