export type StatusOutputRow = {
  icon: string;
  label: string;
  value: string;
  raw: string;
};

export type StatusOutput = {
  titleIcon: string;
  title: string;
  rows: StatusOutputRow[];
};

const TITLE = "Juex status";
const TITLE_ICON = "📊";

const ROW_ICONS: Record<string, string> = Object.assign(Object.create(null), {
  session: "💬",
  "session kind": "📌",
  workdir: "📁",
  provider: "🤖",
  mcp: "🔌",
  skills: "🧩",
  tokens: "🔢",
  context: "🧠",
  turn: "⚙️",
  "queued input": "📥",
});

export function formatStatusOutput(text: unknown): StatusOutput | null {
  if (typeof text !== "string" || !text) return null;
  const lines = text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (lines[0] !== TITLE) return null;

  return {
    title: TITLE,
    titleIcon: TITLE_ICON,
    rows: lines.slice(1).map(formatStatusRow),
  };
}

function formatStatusRow(raw: string): StatusOutputRow {
  const splitAt = raw.indexOf(":");
  const label = splitAt >= 0 ? raw.slice(0, splitAt).trim() : "";
  const value = splitAt >= 0 ? raw.slice(splitAt + 1).trim() : raw;
  return {
    icon: ROW_ICONS[label.toLowerCase()] ?? "✨",
    label,
    value,
    raw,
  };
}
