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
  const charTruncated = content.length > maxChars;

  if (charTruncated) {
    text = text.slice(0, maxChars);
  }

  const { lines, contentLineCount } = splitResultLines(text);
  if (contentLineCount > maxLines) {
    omittedLines = contentLineCount - maxLines;
    text = lines.slice(0, maxLines).join("\n");
  }

  const truncated = charTruncated || omittedLines > 0;
  if (!truncated) {
    return { text, truncated, omittedChars, omittedLines };
  }

  if (charTruncated) {
    omittedChars = content.length - text.length;
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

function splitResultLines(text: string): {
  lines: string[];
  contentLineCount: number;
} {
  const lines = text.split(/\r\n|\r|\n/);
  const hasTrailingNewline =
    text.endsWith("\n") || text.endsWith("\r");
  const contentLineCount =
    hasTrailingNewline && lines.length > 1 && lines[lines.length - 1] === ""
      ? lines.length - 1
      : lines.length;
  return { lines, contentLineCount };
}

function pluralize(count: number, word: string): string {
  return `${count} more ${word}${count === 1 ? "" : "s"}`;
}
