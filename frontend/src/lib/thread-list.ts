import { threadTitle } from "./thread-title.ts";
import { agentPathFromLocation } from "./fleet-routes.ts";

type ThreadListSummary = {
  thread_id: string;
  alias: string;
  retention_state: "active" | "archived";
  turn_count: number;
  generation_count: number;
};

type ThreadAncestry = { thread_id: string; parent_thread_id?: string };

export function threadBatchGroups<T extends ThreadAncestry>(targets: readonly T[], index: ReadonlyMap<string, ThreadAncestry>): T[][] {
  const groups = new Map<number, T[]>();
  for (const thread of targets) {
    const visited = new Set([thread.thread_id]);
    let parent = thread.parent_thread_id;
    let depth = 0;
    while (parent && !visited.has(parent)) {
      visited.add(parent);
      depth += 1;
      parent = index.get(parent)?.parent_thread_id;
    }
    const group = groups.get(depth) ?? [];
    group.push(thread);
    groups.set(depth, group);
  }
  return [...groups.entries()].sort(([left], [right]) => right - left).map(([, group]) => group);
}

export function threadHref(id: string, pathname?: string): string {
  return agentPathFromLocation(
    `/threads/${encodeURIComponent(id)}`,
    pathname,
  );
}

export function threadListTitle(
  thread: Pick<ThreadListSummary, "alias" | "thread_id">,
): string {
  return threadTitle(thread.alias, thread.thread_id);
}

export function threadListBadges(
  thread: Pick<ThreadListSummary, "retention_state" | "turn_count" | "generation_count">,
): string[] {
  const badges: string[] = [thread.retention_state];
  badges.push(`${thread.turn_count} ${thread.turn_count === 1 ? "turn" : "turns"}`);
  badges.push(`${thread.generation_count} gen`);
  return badges;
}
