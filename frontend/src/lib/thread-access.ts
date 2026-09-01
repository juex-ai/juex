type ThreadAccessSummary = {
  active: boolean;
};

export function threadCanSend(thread: ThreadAccessSummary): boolean {
  return thread.active;
}

export function threadReadOnlyMessage(thread: ThreadAccessSummary): string {
  return thread.active ? "" : "Archived thread";
}
