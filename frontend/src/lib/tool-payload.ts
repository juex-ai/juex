export function formatToolPayload(value: unknown, fallback = "{}"): string {
  if (value == null) return fallback;
  try {
    return JSON.stringify(value, null, 2) ?? fallback;
  } catch {
    try {
      return String(value);
    } catch {
      return "[unserializable]";
    }
  }
}
