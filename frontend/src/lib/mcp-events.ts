export type ExternalEventDisplay = {
  label: string;
  content: string;
  preview: string;
  copyText: string;
};

const FALLBACK_LABEL = "mcp:event";

export type MCPEventDisplay = ExternalEventDisplay;

export function formatMCPEventForDisplay(text: string): ExternalEventDisplay {
  return formatExternalEventForDisplay(text, {
    fallbackLabel: FALLBACK_LABEL,
    parseColonPrefix: true,
  });
}

export function formatObservationEventForDisplay(
  text: string,
): ExternalEventDisplay {
  return formatExternalEventForDisplay(text, {
    fallbackLabel: "observation:event",
    parseColonPrefix: false,
  });
}

export function formatExternalEventForDisplay(
  text: string,
  opts: {
    fallbackLabel: string;
    parseColonPrefix: boolean;
  },
): ExternalEventDisplay {
  const event = opts.parseColonPrefix
    ? parseColonEventText(text, opts.fallbackLabel)
    : { label: opts.fallbackLabel, content: text };
  const previewText = paramsContentPreview(event.content) ?? event.content;
  return {
    ...event,
    preview: oneLinePreview(previewText),
    copyText: event.content,
  };
}

export function parseMCPEventText(text: string): {
  label: string;
  content: string;
} {
  return parseColonEventText(text, FALLBACK_LABEL);
}

function parseColonEventText(
  text: string,
  fallbackLabel: string,
): {
  label: string;
  content: string;
} {
  const first = text.indexOf(":");
  const second = first >= 0 ? text.indexOf(":", first + 1) : -1;
  if (first < 0 || second < 0) {
    return { label: fallbackLabel, content: text };
  }

  const source = text.slice(0, first).trim();
  const eventType = text.slice(first + 1, second).trim();
  if (!source || !eventType) {
    return { label: fallbackLabel, content: text };
  }

  return {
    label: `${source}:${eventType}`,
    content: text.slice(second + 1),
  };
}

export function oneLinePreview(text: string, maxLength = 120): string {
  const singleLine = text.replace(/\s+/g, " ").trim();
  if (!singleLine) return "empty event";
  if (singleLine.length <= maxLength) return singleLine;
  return `${singleLine.slice(0, maxLength)}...`;
}

function paramsContentPreview(text: string): string | null {
  try {
    const value = JSON.parse(text) as unknown;
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    const content = (value as { content?: unknown }).content;
    return typeof content === "string" ? content : null;
  } catch {
    return null;
  }
}
