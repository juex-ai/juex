import type { MessageGroup } from "./display-units";

export const COMPACT_COPIED_TOOLTIP = "compacted content copied";

export type CopyTooltipMode = "always" | "copied-only" | "none";

export function copyButtonDefaultTooltipMode({
  hasVisibleLabel,
}: {
  hasVisibleLabel: boolean;
}): CopyTooltipMode {
  return hasVisibleLabel ? "always" : "none";
}

export function compactSummaryText(text: string): string {
  const marker = "Summary of earlier conversation:";
  const markerIndex = text.indexOf(marker);
  if (markerIndex < 0) return text.trim();
  return text.slice(markerIndex + marker.length).trim();
}

export function messageGroupCopyText(group: MessageGroup): string {
  return group.units
    .flatMap((unit) => {
      if (unit.kind === "text") return [unit.block.text];
      if (unit.kind === "reasoning") {
        if (unit.block.redacted) return [];
        return [unit.block.text ?? unit.block.content ?? ""];
      }
      return [];
    })
    .map((part) => part.trim())
    .filter(Boolean)
    .join("\n\n");
}

export function messageGroupCanCopy(group: MessageGroup): boolean {
  if (
    group.kind === "compact" ||
    group.kind === "mcp_event" ||
    group.kind === "observation"
  ) {
    return false;
  }
  return Boolean(messageGroupCopyText(group));
}

export function copyButtonTooltip({
  copied,
  mode = "always",
  idleTooltip,
  copiedTooltip,
}: {
  copied: boolean;
  mode?: CopyTooltipMode;
  idleTooltip: string;
  copiedTooltip: string;
}): string | undefined {
  if (mode === "none") return undefined;
  if (mode === "copied-only") return copiedTooltip;
  return copied ? copiedTooltip : idleTooltip;
}
