import type { TasksSnapshot } from "@/module-schema";
import { formatRuntimeTimestamp } from "@/lib/runtime-display";
import { ModuleStatusControl, ModuleStatusRow } from "../status-presentation";
import type { ModuleStatusProps } from "../types";
import { runtimeTasksBadgeLabel, runtimeTasksIsActive } from "./display";

export function TasksStatus({ state, readOnly, stale }: ModuleStatusProps) {
  const snapshot = state.value as TasksSnapshot | null;
  const tasks = snapshot?.tasks ?? [];
  return <ModuleStatusControl title="Tasks" label={runtimeTasksBadgeLabel(snapshot ?? undefined)} active={runtimeTasksIsActive(snapshot ?? undefined)} readOnly={readOnly} stale={stale}>
    {tasks.length ? <ul className="space-y-3">
      {tasks.map(task => <li key={task.id} className="space-y-1 border-b border-border pb-3 last:border-0 last:pb-0">
        <div className="font-medium [overflow-wrap:anywhere]">{task.title}</div>
        <ModuleStatusRow label="status" value={`${task.status} · ${task.priority}`} />
        <ModuleStatusRow label="description" value={task.description} />
        <ModuleStatusRow label="acceptance" value={task.acceptance || "-"} />
        <ModuleStatusRow label="reason" value={task.status_reason || "-"} />
        <ModuleStatusRow label="continues" value={String(task.continuation_count)} />
        <ModuleStatusRow label="updated" value={formatRuntimeTimestamp(task.updated_at)} />
      </li>)}
    </ul> : <div className="text-muted-foreground">No tasks for this thread.</div>}
  </ModuleStatusControl>;
}
