import type { MessageGroup } from "./display-units";

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
        return [unit.block.text ?? unit.block.content ?? ""];
      }
      return [];
    })
    .map((part) => part.trim())
    .filter(Boolean)
    .join("\n\n");
}
