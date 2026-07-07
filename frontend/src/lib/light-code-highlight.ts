export type LightCodeLanguage = "json" | "log" | "text" | (string & {});

export interface LightCodeToken {
  content: string;
  color?: string;
  darkColor?: string;
}

export interface LightCodeResult {
  tokens: LightCodeToken[][];
  fg: string;
  bg: string;
}

const jsonTokenStyles = {
  literal: {
    color: "var(--juex-info)",
    darkColor: "var(--juex-forest-300)",
  },
  number: {
    color: "var(--juex-gold-900)",
    darkColor: "var(--juex-gold-300)",
  },
  punctuation: {
    color: "var(--juex-ink-600)",
    darkColor: "var(--juex-cream-400)",
  },
  string: {
    color: "var(--juex-forest-700)",
    darkColor: "var(--juex-forest-200)",
  },
} satisfies Record<string, Pick<LightCodeToken, "color" | "darkColor">>;

const jsonNumberPattern = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[Ee][+-]?\d+)?/;
const jsonLiteralPattern = /^(?:true|false|null)\b/;
const jsonPunctuation = new Set(["{", "}", "[", "]", ":", ","]);

const normalizeLanguage = (language: LightCodeLanguage) =>
  language.trim().toLowerCase();

const createRawTokens = (code: string): LightCodeResult => ({
  bg: "transparent",
  fg: "inherit",
  tokens: code.split("\n").map((line) =>
    line === ""
      ? []
      : [
          {
            color: "inherit",
            content: line,
          },
        ]
  ),
});

const readJsonString = (line: string, start: number) => {
  let cursor = start + 1;
  let escaped = false;

  while (cursor < line.length) {
    const char = line[cursor];
    cursor += 1;
    if (escaped) {
      escaped = false;
      continue;
    }
    if (char === "\\") {
      escaped = true;
      continue;
    }
    if (char === '"') {
      break;
    }
  }

  return cursor;
};

const readPlainText = (line: string, start: number) => {
  let cursor = start;
  while (cursor < line.length) {
    const char = line[cursor];
    if (char === '"' || jsonPunctuation.has(char)) {
      break;
    }
    if (line.slice(cursor).match(jsonNumberPattern)) {
      break;
    }
    if (line.slice(cursor).match(jsonLiteralPattern)) {
      break;
    }
    cursor += 1;
  }
  return cursor === start ? start + 1 : cursor;
};

const tokenizeJsonLine = (line: string): LightCodeToken[] => {
  const tokens: LightCodeToken[] = [];
  let cursor = 0;

  while (cursor < line.length) {
    const char = line[cursor];

    if (char === '"') {
      const nextCursor = readJsonString(line, cursor);
      tokens.push({
        content: line.slice(cursor, nextCursor),
        ...jsonTokenStyles.string,
      });
      cursor = nextCursor;
      continue;
    }

    const numberMatch = line.slice(cursor).match(jsonNumberPattern);
    if (numberMatch?.[0]) {
      tokens.push({
        content: numberMatch[0],
        ...jsonTokenStyles.number,
      });
      cursor += numberMatch[0].length;
      continue;
    }

    const literalMatch = line.slice(cursor).match(jsonLiteralPattern);
    if (literalMatch?.[0]) {
      tokens.push({
        content: literalMatch[0],
        ...jsonTokenStyles.literal,
      });
      cursor += literalMatch[0].length;
      continue;
    }

    if (jsonPunctuation.has(char)) {
      tokens.push({
        content: char,
        ...jsonTokenStyles.punctuation,
      });
      cursor += 1;
      continue;
    }

    const nextCursor = readPlainText(line, cursor);
    tokens.push({ content: line.slice(cursor, nextCursor) });
    cursor = nextCursor;
  }

  return tokens;
};

const createJsonTokens = (code: string): LightCodeResult => ({
  bg: "transparent",
  fg: "inherit",
  tokens: code.split("\n").map((line) =>
    line === "" ? [] : tokenizeJsonLine(line)
  ),
});

export const highlightLightCode = (
  code: string,
  language: LightCodeLanguage
): LightCodeResult => {
  const normalizedLanguage = normalizeLanguage(language);
  if (
    normalizedLanguage === "json" ||
    normalizedLanguage === "jsonc" ||
    normalizedLanguage === "jsonl"
  ) {
    return createJsonTokens(code);
  }
  return createRawTokens(code);
};
