import { createHighlighterCore } from 'shiki/core';
import { createOnigurumaEngine } from 'shiki/engine/oniguruma';

// This module loads only when a code preview opens. Tool/transcript highlighting stays lightweight.
const highlighter = createHighlighterCore({
  themes: [import('shiki/themes/github-light.mjs'), import('shiki/themes/dark-plus.mjs')],
  langs: [
    import('shiki/langs/javascript.mjs'), import('shiki/langs/typescript.mjs'),
    import('shiki/langs/jsx.mjs'), import('shiki/langs/tsx.mjs'), import('shiki/langs/json.mjs'),
    import('shiki/langs/html.mjs'), import('shiki/langs/css.mjs'), import('shiki/langs/python.mjs'),
    import('shiki/langs/go.mjs'), import('shiki/langs/markdown.mjs'), import('shiki/langs/yaml.mjs'),
    import('shiki/langs/shellscript.mjs'),
  ],
  engine: createOnigurumaEngine(import('shiki/wasm')),
});

export async function highlightFile(code: string, language: string) {
  return (await highlighter).codeToTokens(code, {
    lang: language, themes: { light: 'github-light', dark: 'dark-plus' },
  }).tokens;
}
