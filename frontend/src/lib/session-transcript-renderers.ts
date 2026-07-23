import { LOCAL_COMPACT_PENDING_KIND } from "./compact-ui.ts";
import type { MessageGroup } from "./display-units.ts";

export const MESSAGE_GROUP_RENDERER_KEYS = [
  "default",
  "mcp_event",
  "observation",
  "hook_event",
  "model_fallback",
  "system_notice",
  "compact",
  LOCAL_COMPACT_PENDING_KIND,
] as const;

export type MessageGroupRendererKey =
  (typeof MESSAGE_GROUP_RENDERER_KEYS)[number];

const messageGroupRendererKeySet = new Set<string>(
  MESSAGE_GROUP_RENDERER_KEYS,
);

export function messageGroupRendererKey(
  group: Pick<MessageGroup, "kind">,
): MessageGroupRendererKey {
  const kind = group.kind ?? "";
  if (kind && messageGroupRendererKeySet.has(kind)) {
    return kind as MessageGroupRendererKey;
  }
  return "default";
}
