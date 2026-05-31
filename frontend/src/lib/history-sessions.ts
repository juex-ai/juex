type HistorySessionSummary = {
  id: string;
  preview: string;
  kind: "primary" | "side";
  active: boolean;
  turns: number;
};

export function historySessionHref(id: string): string {
  return `/sessions/${encodeURIComponent(id)}`;
}

export function historySessionTitle(
  session: Pick<HistorySessionSummary, "preview">,
): string {
  return session.preview.trim() || "(empty)";
}

export function historySessionBadges(
  session: Pick<HistorySessionSummary, "kind" | "active" | "turns">,
): string[] {
  const badges: string[] = [session.kind];
  if (session.active) badges.push("active");
  badges.push(`${session.turns} ${session.turns === 1 ? "turn" : "turns"}`);
  return badges;
}
