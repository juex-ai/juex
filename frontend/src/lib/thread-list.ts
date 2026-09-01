import { threadTitle } from "./thread-title.ts";
import { agentPathFromLocation } from "./fleet-routes.ts";

type ThreadListSummary = {
  thread_id: string;
  alias: string;
  archived_at?: string;
  turn_count: number;
  generation_count: number;
};

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
  thread: Pick<ThreadListSummary, "archived_at" | "turn_count" | "generation_count">,
): string[] {
  const badges: string[] = [thread.archived_at ? "archived" : "active"];
  badges.push(`${thread.turn_count} ${thread.turn_count === 1 ? "turn" : "turns"}`);
  badges.push(`${thread.generation_count} gen`);
  return badges;
}
