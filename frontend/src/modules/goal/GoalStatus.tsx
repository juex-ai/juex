import type { GoalStatusSnapshot } from "@/module-schema";
import { formatRuntimeTimestamp } from "@/lib/runtime-display";
import { ModuleStatusControl, ModuleStatusRow } from "../status-presentation";
import type { ModuleStatusProps } from "../types";
import { runtimeGoalBadgeLabel, runtimeGoalContinuationLabel, runtimeGoalIsActive } from "./display";

export function GoalStatus({ state, readOnly, stale }: ModuleStatusProps) {
  const goal = state.value as GoalStatusSnapshot | null;
  return <ModuleStatusControl title="Goal" label={runtimeGoalBadgeLabel(goal ?? undefined)} active={runtimeGoalIsActive(goal ?? undefined)} readOnly={readOnly} stale={stale}>
    {goal ? <>
      <ModuleStatusRow label="status" value={goal.status || "unknown"} />
      <ModuleStatusRow label="description" value={goal.description || "-"} />
      <ModuleStatusRow label="acceptance" value={goal.acceptance || "-"} />
      <ModuleStatusRow label="reason" value={goal.status_reason || "-"} />
      <ModuleStatusRow label="continuations" value={runtimeGoalContinuationLabel(goal)} />
      <ModuleStatusRow label="updated" value={formatRuntimeTimestamp(goal.updated_at)} />
    </> : <div className="text-muted-foreground">No goal state for this thread.</div>}
  </ModuleStatusControl>;
}
