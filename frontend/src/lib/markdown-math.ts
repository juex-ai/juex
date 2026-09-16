import remarkParse from "remark-parse";
import { unified } from "unified";

const parser = unified().use(remarkParse);
type MarkdownNode = {
  type: string;
  position?: { start: { offset?: number }; end: { offset?: number } };
  children?: MarkdownNode[];
};
const literalTypes = new Set(["code", "inlineCode", "html", "definition", "link", "image"]);

// Normalize before Streamdown splits blocks or CommonMark consumes backslashes.
// Parser offsets protect code (including nested fences), HTML, and link targets.
export function normalizeMathDelimiters(markdown: string): string {
  if (!/\\[([]/.test(markdown)) return markdown;
  const ranges: [number, number][] = [];
  function collect(node: MarkdownNode) {
    const start = node.position?.start.offset;
    const end = node.position?.end.offset;
    if (literalTypes.has(node.type) && start !== undefined && end !== undefined) {
      ranges.push([start, end]);
      return;
    }
    node.children?.forEach(collect);
  }
  collect(parser.parse(markdown));

  let cursor = 0;
  const parts: string[] = [];
  for (const [start, end] of ranges) {
    parts.push(normalizeProse(markdown.slice(cursor, start)), markdown.slice(start, end));
    cursor = end;
  }
  parts.push(normalizeProse(markdown.slice(cursor)));
  return parts.join("");
}

function normalizeProse(source: string): string {
  // Consume existing dollar math and escaped backslashes before LaTeX delimiters.
  return source.replace(
    /\$\$[\s\S]*?\$\$|\\\((?:\\[\s\S]|[^\\])*?\\\)|\\\[(?:\\[\s\S]|[^\\])*?\\\]|\\[\s\S]/g,
    (match) => {
      if (!match.startsWith("\\(") && !match.startsWith("\\[")) return match;
      if (match.length === 2) return match;
      const body = match.slice(2, -2);
      if (!body.trim() || /\r?\n\s*\r?\n/.test(body)) return match;
      // Double dollars also delimit inline math in the existing math plugin;
      // keeping single-dollar parsing disabled avoids interpreting prices as math.
      return match.startsWith("\\[")
        ? `$$\n${body.trim()}\n$$`
        : `$$${body.replace(/\r?\n/g, " ")}$$`;
    },
  );
}
