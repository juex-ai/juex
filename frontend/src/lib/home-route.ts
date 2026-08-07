import { historySessionHref } from "./history-sessions.ts";

export function homeActiveSessionHref(
  activeSessionID?: string | null,
  pathname?: string,
): string | null {
  return activeSessionID
    ? historySessionHref(activeSessionID, pathname)
    : null;
}
