type ThreadAccessSummary = {
  retention_state: "active" | "archived";
};

export function threadCanSend(thread: ThreadAccessSummary): boolean {
  return thread.retention_state === "active";
}

export function threadReadOnlyMessage(thread: ThreadAccessSummary): string {
  return thread.retention_state === "active" ? "" : "Archived thread";
}
