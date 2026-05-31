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

export function toolDisplayName(type: string, toolName?: string): string {
  const name = type === "dynamic-tool"
    ? toolName
    : type.startsWith("tool-")
      ? type.slice("tool-".length)
      : type;

  return name && name.trim() ? name : "tool";
}

export function toolTimeoutLabel(timeoutSeconds: number | undefined): string | undefined {
  if (!Number.isFinite(timeoutSeconds) || !timeoutSeconds || timeoutSeconds <= 0) {
    return undefined;
  }
  return `timeout ${Math.round(timeoutSeconds)}s`;
}
