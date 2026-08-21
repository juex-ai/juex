import { LOCAL_COMPACT_PENDING_KIND } from "./compact-ui.ts";
import type { MessageGroup } from "./display-units.ts";

export const MESSAGE_KINDS = [
  "direct",
  "continuation",
  "tool_result",
  "mcp_event",
  "observation",
  "side_session",
  "policy_event",
  "compact",
  "runtime_context",
  "model_change",
  "system_notice",
] as const;

export type MessageKind = (typeof MESSAGE_KINDS)[number];

export const MESSAGE_GROUP_RENDERER_KEYS = [
  "default",
  "mcp_event",
  "observation",
  "side_session",
  "policy_event",
  "model_fallback",
  "system_notice",
  "compact",
  LOCAL_COMPACT_PENDING_KIND,
] as const;

export type MessageGroupRendererKey =
  (typeof MESSAGE_GROUP_RENDERER_KEYS)[number];

const messageKindRendererKeys: Record<MessageKind, MessageGroupRendererKey> = {
  direct: "default",
  continuation: "default",
  tool_result: "default",
  mcp_event: "mcp_event",
  observation: "observation",
  side_session: "side_session",
  policy_event: "policy_event",
  compact: "compact",
  runtime_context: "default",
  model_change: "model_fallback",
  system_notice: "system_notice",
};

const messageKindSet = new Set<string>(MESSAGE_KINDS);
const userOnlyRendererKeySet = new Set<MessageGroupRendererKey>([
  "mcp_event",
  "observation",
  "side_session",
  "model_fallback",
  "system_notice",
]);

export function messageGroupRendererKey(
  group: Pick<MessageGroup, "kind" | "role">,
): MessageGroupRendererKey {
  const kind = group.kind ?? "";
  if (kind === LOCAL_COMPACT_PENDING_KIND) {
    return LOCAL_COMPACT_PENDING_KIND;
  }
  if (!messageKindSet.has(kind)) {
    return "default";
  }
  const rendererKey = messageKindRendererKeys[kind as MessageKind];
  if (userOnlyRendererKeySet.has(rendererKey) && group.role !== "user") {
    return "default";
  }
  return rendererKey;
}
