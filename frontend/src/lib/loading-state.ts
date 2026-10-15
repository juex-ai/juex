export function loadingStateLabel(label: string | null | undefined): string {
  const trimmed = label?.trim();
  return trimmed || "Loading";
}
