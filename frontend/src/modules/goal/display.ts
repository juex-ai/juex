import type { GoalStatusSnapshot } from "../../module-schema";

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

