import type { TasksSnapshot } from "../../module-schema";

export function runtimeTasksBadgeLabel(state?: TasksSnapshot): string {
  const tasks = state?.tasks ?? [];
  if (!tasks.length) return "tasks empty";
  return `tasks ${tasks.filter(task => task.status === "done").length}/${tasks.length}`;
}

export function runtimeTasksIsActive(state?: TasksSnapshot): boolean {
  return Boolean(state?.tasks.some(task => task.status === "todo" || task.status === "doing"));
}
