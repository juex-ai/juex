export type ObservationTimestamp = number | string | null | undefined;

export function formatObservationTimestamp(value: ObservationTimestamp): string {
  if (
    value === null ||
    value === undefined ||
    value === "" ||
    value === 0 ||
    value === "0"
  ) {
    return "-";
  }
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return String(value);
  const dateText = [
    date.getFullYear().toString().padStart(4, "0"),
    pad2(date.getMonth() + 1),
    pad2(date.getDate()),
  ].join("");
  const timeText = [
    pad2(date.getHours()),
    pad2(date.getMinutes()),
    pad2(date.getSeconds()),
  ].join(":");
  return `${dateText} ${timeText}`;
}

export function formatObservationWindow(
  start: ObservationTimestamp,
  end: ObservationTimestamp,
): string {
  const startText = formatObservationTimestamp(start);
  const endText = formatObservationTimestamp(end);
  if (startText === endText || endText === "-") return startText;
  return `${startText} - ${endText}`;
}

function pad2(value: number): string {
  return value.toString().padStart(2, "0");
}
