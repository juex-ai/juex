import { sessionPreviewTitle } from "./session-title.ts";
import { agentPathFromLocation } from "./fleet-routes.ts";

type HistorySessionSummary = {
  id: string;
  preview: string;
  kind: "primary" | "side";
  active: boolean;
  turns: number;
};

export function historySessionHref(id: string, pathname?: string): string {
  return agentPathFromLocation(
    `/sessions/${encodeURIComponent(id)}`,
    pathname,
  );
}

export function historySessionTitle(
  session: Pick<HistorySessionSummary, "preview">,
): string {
  return sessionPreviewTitle(session.preview);
}

export function historySessionBadges(
  session: Pick<HistorySessionSummary, "kind" | "active" | "turns">,
): string[] {
  const badges: string[] = [session.kind];
  if (session.active) badges.push("active");
  badges.push(`${session.turns} ${session.turns === 1 ? "turn" : "turns"}`);
  return badges;
}
