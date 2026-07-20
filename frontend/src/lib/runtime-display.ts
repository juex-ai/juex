import type {
  ContextUsage,
  GoalStatusSnapshot,
  NotesSnapshot,
  RuntimeHooksStatus,
  TokenUsage,
} from "../types";

export function formatRuntimeTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0";
  if (value < 1000) return String(value);
  if (value < 1_000_000) {
    const thousands = Math.round(value / 100) / 10;
    if (thousands < 1000) {
      return `${thousands}k`;
    }
  }
  return `${Math.round(value / 100_000) / 10}m`;
}

export function runtimeContextPercentLabel(usage?: ContextUsage): string {
  const windowTokens = usage?.context_window ?? 0;
  const totalTokens = usage?.total_tokens ?? 0;
  if (!Number.isFinite(windowTokens) || windowTokens <= 0) return "-";
  if (!Number.isFinite(totalTokens) || totalTokens <= 0) return "~0%";
  return `~${formatRuntimePercent((totalTokens / windowTokens) * 100)}`;
}

export function runtimeContextModelLabel(usage?: ContextUsage): string {
  return usage?.model?.trim() || "unknown";
}

export function runtimeContextWindowDetailLabel(
  usage?: ContextUsage | null,
): string {
  if (!usage) return "context window: 0/0 tokens (0%)";
  const windowTokens = usage?.context_window ?? 0;
  const totalTokens = usage?.total_tokens ?? 0;
  const percent =
    windowTokens > 0 ? (totalTokens / windowTokens) * 100 : 0;
  const current = formatRuntimeTokenCount(totalTokens);
  const window = formatRuntimeTokenCount(windowTokens);
  return `context window: ~${current}/${window} tokens (~${formatRuntimePercent(percent)})`;
}

export function runtimeTokenUsageDetailLabel(usage?: TokenUsage): string {
  const input = usage?.input_tokens ?? 0;
  const output = usage?.output_tokens ?? 0;
  const inputLabel = formatRuntimeTokenCount(input);
  const outputLabel = formatRuntimeTokenCount(output);
  return `total tokens: ${inputLabel} in / ${outputLabel} out`;
}

function formatRuntimePercent(value: number): string {
  if (!Number.isFinite(value)) return "-";
  const rounded = Math.round(value * 10) / 10;
  return `${rounded}%`;
}

export function runtimeHooksSummaryLabel(hooks?: RuntimeHooksStatus): string {
  const count = hooks?.configured ?? 0;
  return `${count} ${count === 1 ? "hook" : "hooks"}`;
}

export function runtimeHookCommandLabel(command?: string[]): string {
  if (!command || command.length === 0) return "-";
  return command.join(" ");
}

export function runtimeGoalBadgeLabel(goal?: GoalStatusSnapshot): string {
  return `goal ${goal?.status || "none"}`;
}

export function runtimeGoalIsActive(goal?: GoalStatusSnapshot): boolean {
  return Boolean(goal?.status && goal.status !== "none");
}

export function runtimeGoalContinuationLabel(goal?: GoalStatusSnapshot): string {
  if (!goal) return "-";
  return String(goal.continuation_count ?? 0);
}

export function runtimeSessionStateBadgeLabel(
  goal?: GoalStatusSnapshot,
  notes?: NotesSnapshot,
): string {
  if (runtimeGoalIsActive(goal)) {
    return `goal ${goal?.status}`;
  }
  const progress = notesCheckboxProgress(notes);
  if (progress.total > 0) {
    return `notes ${progress.completed}/${progress.total}`;
  }
  if (notes?.content?.trim()) {
    return "notes active";
  }
  return "goal idle";
}

export function runtimeSessionStateIsActive(
  goal?: GoalStatusSnapshot,
  notes?: NotesSnapshot,
): boolean {
  return runtimeGoalIsActive(goal) || Boolean(notes?.content?.trim());
}

export interface NotesCheckboxProgress {
  completed: number;
  total: number;
  percent: number;
}

export function notesCheckboxProgress(notes?: NotesSnapshot): NotesCheckboxProgress {
  let completed = 0;
  let total = 0;
  for (const match of notes?.content?.matchAll(/^\s*-\s+\[([ xX])\]\s+/gm) ?? []) {
    total += 1;
    if (match[1].toLowerCase() === "x") completed += 1;
  }
  return { completed, total, percent: total > 0 ? (completed / total) * 100 : 0 };
}

export function formatRuntimeTimestamp(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}
