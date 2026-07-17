export type ModelFallbackDisplay = {
  title: string;
  content: string;
};

export function formatModelFallbackNotice(text: string): ModelFallbackDisplay {
  const content = text
    .trim()
    .replace(/^<system-reminder>\s*/i, "")
    .replace(/\s*<\/system-reminder>$/i, "")
    .trim();
  return {
    title: /healthy again|recovered|switched back/i.test(content)
      ? "Model recovered"
      : "Model switched",
    content,
  };
}
