import type {
  ContextUsage,
  GoalStatusSnapshot,
  RuntimeHooksStatus,
  TokenUsage,
  WorkingState,
  WorkingStateRecord,
  WorkingStateStatusSnapshot,
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

export type WorkingStateSectionKey =
  | "goal"
  | "hard_constraints"
  | "artifacts"
  | "checks"
  | "open_issues"
  | "tool_failures"
  | "last_successful_checks"
  | "stale_checks"
  | "active_processes"
  | "runtime_budget";

export interface WorkingStateSectionDefinition {
  key: WorkingStateSectionKey;
  label: string;
}

export const WORKING_STATE_SECTIONS: WorkingStateSectionDefinition[] = [
  { key: "goal", label: "Observed summary" },
  { key: "hard_constraints", label: "Hard constraints" },
  { key: "artifacts", label: "Artifacts" },
  { key: "checks", label: "Checks" },
  { key: "open_issues", label: "Open issues" },
  { key: "tool_failures", label: "Tool failures" },
  { key: "active_processes", label: "Active processes" },
  { key: "runtime_budget", label: "Runtime budget" },
  { key: "last_successful_checks", label: "Last successful checks" },
  { key: "stale_checks", label: "Stale checks" },
];

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

export function workingStatePresenceLabel(
  snapshot?: WorkingStateStatusSnapshot,
): string {
  if (!snapshot) return "no active session";
  if (snapshot.disabled) return "disabled";
  if (snapshot.present) return "present";
  return "empty";
}

export function workingStateSectionCounts(snapshot?: WorkingStateStatusSnapshot): {
  key: WorkingStateSectionKey;
  label: string;
  count: number;
}[] {
  return WORKING_STATE_SECTIONS.map((section) => ({
    ...section,
    count: workingStateRecords(snapshot?.state, section.key).length,
  }));
}

export function workingStateRecordCount(snapshot?: WorkingStateStatusSnapshot): number {
  return workingStateSectionCounts(snapshot).reduce((sum, item) => sum + item.count, 0);
}

export function runtimeWorkingStateBadgeLabel(
  snapshot?: WorkingStateStatusSnapshot,
): string {
  const count = workingStateRecordCount(snapshot);
  if (count > 0) return `state ${count}`;
  if (snapshot?.disabled) return "state off";
  if (!snapshot) return "state none";
  return `state ${workingStatePresenceLabel(snapshot)}`;
}

export function runtimeSessionStateBadgeLabel(): string {
  return "state";
}

export function runtimeSessionStateIsActive(
  goal?: GoalStatusSnapshot,
  snapshot?: WorkingStateStatusSnapshot,
): boolean {
  return runtimeGoalIsActive(goal) || Boolean(snapshot?.present);
}

export function workingStateRecords(
  state: WorkingState | undefined,
  key: WorkingStateSectionKey,
): WorkingStateRecord[] {
  if (!state) return [];
  switch (key) {
    case "goal":
      return state.goal ? [state.goal] : [];
    case "hard_constraints":
      return state.hard_constraints ?? [];
    case "artifacts":
      return state.artifacts ?? [];
    case "checks":
      return state.checks ?? [];
    case "open_issues":
      return state.open_issues ?? [];
    case "tool_failures":
      return state.tool_failures ?? [];
    case "last_successful_checks":
      return state.last_successful_checks ?? [];
    case "stale_checks":
      return state.stale_checks ?? [];
    case "active_processes":
      return state.active_processes ?? [];
    case "runtime_budget":
      return state.runtime_budget ?? [];
  }
}

export function formatRuntimeTimestamp(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}
