import type { ToolUIPartState } from "../components/ai-elements/_local-types";

const STATUS_LABELS: Record<ToolUIPartState, string> = {
  "approval-requested": "approval",
  "approval-responded": "responded",
  "input-available": "running",
  "input-streaming": "pending",
  "output-available": "success",
  "output-denied": "denied",
  "output-error": "failed",
};

export function toolStatusLabel(status: ToolUIPartState): string {
  return STATUS_LABELS[status];
}

export type ToolProcessStatus = "running" | "failed" | "done";

export function toolProcessStatus(status: ToolUIPartState): ToolProcessStatus {
  switch (status) {
    case "output-available":
      return "done";
    case "output-denied":
    case "output-error":
      return "failed";
    default:
      return "running";
  }
}

export function aggregateToolProcessStatus(
  statuses: readonly ToolUIPartState[],
): ToolProcessStatus {
  const compact = statuses.map(toolProcessStatus);
  if (compact.includes("running")) return "running";
  if (compact.includes("failed")) return "failed";
  return "done";
}

export function formatToolBatchTitle(names: readonly string[]): string {
  const counts = new Map<string, number>();
  for (const rawName of names) {
    const name = typeof rawName === "string" && rawName.trim() ? rawName : "tool";
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  return Array.from(counts.entries())
    .map(([name, count]) => `${count} ${name}`)
    .join(", ");
}

export function compactThinkingPreview(text: string, limit = 20): string {
  const value = text.trim();
  if (value.length <= limit) return value;
  return `${value.slice(0, limit)}...`;
}

export function toolDisplayName(type: unknown, toolName?: unknown): string {
  const name =
    type === "dynamic-tool"
      ? toolName
      : typeof type === "string" && type.startsWith("tool-")
        ? type.slice("tool-".length)
        : type;

  return typeof name === "string" && name.trim() ? name : "tool";
}

export function toolTimeoutLabel(timeoutSeconds: number | undefined): string | undefined {
  if (!Number.isFinite(timeoutSeconds) || !timeoutSeconds || timeoutSeconds <= 0) {
    return undefined;
  }
  return `timeout ${Math.round(timeoutSeconds)}s`;
}
