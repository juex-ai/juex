import { threadHref } from "./thread-list.ts";

export function homeActiveThreadHref(
  activeThreadID?: string | null,
  pathname?: string,
): string | null {
  return activeThreadID
    ? threadHref(activeThreadID, pathname)
    : null;
}
