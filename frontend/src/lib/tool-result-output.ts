export const TOOL_RESULT_TEXT_MAX_CHARS = 12000;
export const TOOL_RESULT_TEXT_MAX_LINES = 120;

export type ToolResultTextLimits = {
  maxChars?: number;
  maxLines?: number;
};

export type FormattedToolResultText = {
  text: string;
  truncated: boolean;
  omittedChars: number;
  omittedLines: number;
};

export function formatToolResultText(
  content: string,
  limits: ToolResultTextLimits = {}
): FormattedToolResultText {
  const maxChars = positiveLimit(limits.maxChars, TOOL_RESULT_TEXT_MAX_CHARS);
  const maxLines = positiveLimit(limits.maxLines, TOOL_RESULT_TEXT_MAX_LINES);
  let text = content;
  let omittedChars = 0;
  let omittedLines = 0;

  if (text.length > maxChars) {
    omittedChars = text.length - maxChars;
    text = text.slice(0, maxChars);
  }

  const lines = text.split(/\r\n|\r|\n/);
  if (lines.length > maxLines) {
    omittedLines = lines.length - maxLines;
    text = lines.slice(0, maxLines).join("\n");
  }

  const truncated = omittedChars > 0 || omittedLines > 0;
  if (!truncated) {
    return { text, truncated, omittedChars, omittedLines };
  }

  const reasons = [
    omittedLines > 0 ? pluralize(omittedLines, "line") : undefined,
    omittedChars > 0 ? pluralize(omittedChars, "character") : undefined,
  ].filter(Boolean);
  const separator = text.endsWith("\n") ? "" : "\n";

  return {
    text: `${text}${separator}[tool result truncated: ${reasons.join(", ")}]`,
    truncated,
    omittedChars,
    omittedLines,
  };
}

function positiveLimit(value: number | undefined, fallback: number): number {
  if (value === undefined || !Number.isFinite(value) || value < 1) {
    return fallback;
  }
  return Math.floor(value);
}

function pluralize(count: number, word: string): string {
  return `${count} more ${word}${count === 1 ? "" : "s"}`;
}
