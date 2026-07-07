import { juexCodeThemes } from "./code-theme";
import {
  highlightLightCode,
  type LightCodeLanguage,
} from "./light-code-highlight";
import type { CodeHighlighterPlugin, ThemeInput } from "streamdown";

type StreamdownToken = {
  content: string;
  color?: string;
  htmlStyle?: Record<string, string>;
};

type StreamdownHighlightResult = {
  bg?: string;
  fg?: string;
  tokens: StreamdownToken[][];
};

const toStreamdownResult = (
  code: string,
  language: LightCodeLanguage
): StreamdownHighlightResult => {
  const result = highlightLightCode(code, language);
  return {
    bg: result.bg,
    fg: result.fg,
    tokens: result.tokens.map((line) =>
      line.map((token) => ({
        color: token.color,
        content: token.content,
        htmlStyle: token.darkColor
          ? { "--shiki-dark": token.darkColor }
          : undefined,
      }))
    ),
  };
};

export const streamdownCodePlugin: CodeHighlighterPlugin = {
  getSupportedLanguages: () =>
    ["json", "jsonc", "jsonl", "log", "text"] as ReturnType<
      CodeHighlighterPlugin["getSupportedLanguages"]
    >,
  getThemes: () => juexCodeThemes as [ThemeInput, ThemeInput],
  highlight: ({ code, language }) => toStreamdownResult(code, language),
  name: "shiki",
  supportsLanguage: () => true,
  type: "code-highlighter",
};
