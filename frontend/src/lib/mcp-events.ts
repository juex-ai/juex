export type ExternalEventDisplay = {
  label: string;
  content: string;
  preview: string;
  copyText: string;
};

export type ObservationAttachmentDisplay = {
  kind: "image" | "file";
  sourcePath: string;
  artifactPath: string;
  mediaType: string;
  bytes: number;
};

export type ObservationEventDisplay = ExternalEventDisplay & {
  attachments: ObservationAttachmentDisplay[];
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
): ObservationEventDisplay {
  const envelope = parseObservationTextEnvelope(text);
  if (envelope) {
    return {
      label: observationLabel(envelope.observableID),
      content: envelope.detail,
      preview: oneLinePreview(observationContentPreview(envelope.content)),
      copyText: text,
      attachments: parseObservationAttachments(envelope.attachmentFooter),
    };
  }

  const json = parseJSONRecord(text);
  const observableID = stringField(json, "observable_id");
  const content = stringField(json, "content") ?? text;
  return {
    label: observationLabel(observableID),
    content: text,
    preview: oneLinePreview(observationContentPreview(content)),
    copyText: text,
    attachments: [],
  };
}

export function formatWorkerThreadEventForDisplay(
  text: string,
): ExternalEventDisplay {
  const prefix = "Worker Thread result:";
  const trimmed = text.trim();
  const payload = trimmed.startsWith(prefix)
    ? trimmed.slice(prefix.length).trim()
    : trimmed;
  let previewText = payload;
  try {
    const value = JSON.parse(payload) as unknown;
    if (value && typeof value === "object" && !Array.isArray(value)) {
      const result = value as { output?: unknown; error?: unknown };
      if (typeof result.output === "string" && result.output.trim()) {
        previewText = result.output;
      } else if (typeof result.error === "string" && result.error.trim()) {
        previewText = result.error;
      }
    }
  } catch {
    // Keep the raw payload as the preview when an older message is not JSON.
  }
  return {
    label: "worker_thread:result",
    content: text,
    preview: oneLinePreview(previewText),
    copyText: text,
  };
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

function parseObservationTextEnvelope(text: string): {
  observableID: string | null;
  content: string;
  detail: string;
  attachmentFooter: string;
} | null {
  const firstLineEnd = text.search(/\r?\n/);
  const firstLine = (
    firstLineEnd < 0 ? text : text.slice(0, firstLineEnd)
  ).trim();
  if (firstLine !== "Observable observation") return null;

  const detailStart = firstLineEnd < 0
    ? text.length
    : firstLineEnd + (text[firstLineEnd] === "\r" ? 2 : 1);
  const detail = text.slice(detailStart);
  const contentMarker = detail.match(/(?:^|\r?\n)content:\r?\n/);
  const metadata = contentMarker?.index === undefined
    ? detail
    : detail.slice(0, contentMarker.index);
  const contentStart = contentMarker?.index === undefined
    ? detail.length
    : contentMarker.index + contentMarker[0].length;
  const contentEnd = observationAttachmentSectionStart(detail, contentStart);

  return {
    observableID: lineValue(metadata, "observable_id"),
    content: detail.slice(contentStart, contentEnd).trim(),
    detail,
    attachmentFooter: contentEnd < detail.length ? detail.slice(contentEnd) : "",
  };
}

function observationAttachmentSectionStart(text: string, after: number): number {
  const pattern =
    /\r?\nattachments:\r?\n(?=- (?:image|file) source=.*? artifact=\S+ \([^\r\n]+\)(?:\r?\n|$))/g;
  let found = text.length;
  for (const match of text.matchAll(pattern)) {
    if ((match.index ?? -1) >= after) found = match.index ?? found;
  }
  return found;
}

function parseObservationAttachments(text: string): ObservationAttachmentDisplay[] {
  const attachments: ObservationAttachmentDisplay[] = [];
  const pattern =
    /^- (image|file) source=(.*?) artifact=(\S+) \(([^,\r\n]+), (\d+) bytes(?:,[^\r\n)]*)?\)$/gm;
  for (const match of text.matchAll(pattern)) {
    attachments.push({
      kind: match[1] as ObservationAttachmentDisplay["kind"],
      sourcePath: match[2],
      artifactPath: match[3],
      mediaType: match[4].trim(),
      bytes: Number(match[5]),
    });
  }
  return attachments;
}

function observationContentPreview(content: string): string {
  const trimmed = content.trim();
  if (!trimmed) return content;

  const mcpContent = trimmed.startsWith("MCP notification")
    ? namedTextSection(trimmed, "content", [
        "meta",
        "params",
        "attachments",
        "attachment_errors",
      ])
    : null;
  const candidate = mcpContent?.trim() || trimmed;
  const jsonContent = stringField(parseJSONRecord(candidate), "content");
  return jsonContent?.trim() || candidate;
}

function namedTextSection(
  text: string,
  name: string,
  followingSections: string[],
): string | null {
  const marker = new RegExp(`(?:^|\\r?\\n)${name}:\\r?\\n`);
  const match = marker.exec(text);
  if (!match) return null;
  const start = match.index + match[0].length;
  let end = text.length;
  for (const following of followingSections) {
    const next = new RegExp(`\\r?\\n${following}:\\r?\\n`, "g");
    next.lastIndex = start;
    const nextMatch = next.exec(text);
    if (nextMatch && nextMatch.index < end) end = nextMatch.index;
  }
  return text.slice(start, end);
}

function lineValue(text: string, name: string): string | null {
  const match = new RegExp(`(?:^|\\r?\\n)${name}:\\s*([^\\r\\n]+)`).exec(text);
  return match?.[1]?.trim() || null;
}

function parseJSONRecord(text: string): Record<string, unknown> | null {
  try {
    const value = JSON.parse(text) as unknown;
    return value && typeof value === "object" && !Array.isArray(value)
      ? value as Record<string, unknown>
      : null;
  } catch {
    return null;
  }
}

function stringField(
  value: Record<string, unknown> | null,
  name: string,
): string | null {
  const field = value?.[name];
  return typeof field === "string" && field.trim() ? field : null;
}

function observationLabel(observableID: string | null): string {
  return observableID ? `observation:${observableID}` : "observation:event";
}
