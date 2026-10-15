const processMetricNumber = new Intl.NumberFormat("en-US", {
  maximumFractionDigits: 1,
});

export function formatProcessMemory(bytes?: number): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) {
    return "—";
  }
  const gigabyte = 1_000_000_000;
  const divisor = bytes >= gigabyte ? gigabyte : 1_000_000;
  const unit = bytes >= gigabyte ? "GB" : "MB";
  return `${processMetricNumber.format(bytes / divisor)} ${unit}`;
}

export function formatProcessCPU(percent?: number): string {
  if (percent === undefined || !Number.isFinite(percent) || percent < 0) {
    return "—";
  }
  return `${processMetricNumber.format(percent)}%`;
}
