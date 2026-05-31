type SessionAccessSummary = {
  kind: "primary" | "side";
  active: boolean;
};

type SessionAccessOptions = {
  historyMode: boolean;
};

export function sessionCanSend(
  session: SessionAccessSummary,
  options: SessionAccessOptions,
): boolean {
  return !options.historyMode && session.kind === "primary" && session.active;
}

export function sessionReadOnlyMessage(
  session: SessionAccessSummary,
  options: SessionAccessOptions,
): string {
  if (options.historyMode) return "History view is read-only";
  if (session.kind === "primary" && !session.active) {
    return "Inactive primary session";
  }
  return "Side session";
}
