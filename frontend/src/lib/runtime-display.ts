import type {
  GoalStatusSnapshot,
  RuntimeHooksStatus,
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

export type WorkingStateSectionKey =
  | "goal"
  | "hard_constraints"
  | "artifacts"
  | "checks"
  | "open_issues"
  | "last_successful_checks"
  | "stale_checks";

export interface WorkingStateSectionDefinition {
  key: WorkingStateSectionKey;
  label: string;
}

export const WORKING_STATE_SECTIONS: WorkingStateSectionDefinition[] = [
  { key: "goal", label: "Goal" },
  { key: "hard_constraints", label: "Hard constraints" },
  { key: "artifacts", label: "Artifacts" },
  { key: "checks", label: "Checks" },
  { key: "open_issues", label: "Open issues" },
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

export function runtimeGoalBudgetLabel(goal?: GoalStatusSnapshot): string {
  const budget = goal?.budget;
  if (!budget) return "-";
  const used = budget.continuations_used ?? 0;
  const max = budget.max_continuations ?? 0;
  if (max > 0) return `${used}/${max}`;
  return String(used);
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
    case "last_successful_checks":
      return state.last_successful_checks ?? [];
    case "stale_checks":
      return state.stale_checks ?? [];
  }
}

export function formatRuntimeTimestamp(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}
