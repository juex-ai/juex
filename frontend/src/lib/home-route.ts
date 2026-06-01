import { historySessionHref } from "./history-sessions.ts";

type HomeRouteSession = {
  id: string;
  kind: "primary" | "side";
  active: boolean;
};

export function homeActiveSessionHref(
  sessions?: readonly HomeRouteSession[] | null,
): string | null {
  const active = sessions?.find((session) => (
    session.kind === "primary" && session.active
  ));
  return active ? historySessionHref(active.id) : null;
}
