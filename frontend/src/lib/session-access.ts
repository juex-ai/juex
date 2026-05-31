type SessionAccessSummary = {
  kind: "primary" | "side";
  active: boolean;
};

export function sessionCanSend(session: SessionAccessSummary): boolean {
  return session.kind === "primary" && session.active;
}

export function sessionReadOnlyMessage(session: SessionAccessSummary): string {
  if (session.kind === "primary" && !session.active) {
    return "Inactive primary session";
  }
  return "Side session";
}
