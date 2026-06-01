export function formatRuntimeTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0";
  if (value < 1000) return String(value);
  if (value < 1_000_000) {
    const thousands = Math.round(value / 100) / 10;
    if (thousands < 1000) {
      return `${thousands}k`;
    }
  }
  return `${Math.round(value / 100_000) / 10}m`;
}
