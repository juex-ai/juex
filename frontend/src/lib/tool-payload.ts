export function formatToolPayload(value: unknown, fallback = "{}"): string {
  if (value === undefined) return fallback;
  try {
    return JSON.stringify(value, null, 2) ?? fallback;
  } catch {
    return String(value);
  }
}
