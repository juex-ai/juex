import remarkParse from "remark-parse";
import { unified } from "unified";

const parser = unified().use(remarkParse);
type MarkdownNode = {
  type: string;
  position?: { start: { offset?: number }; end: { offset?: number } };
  children?: MarkdownNode[];
};
const literalTypes = new Set(["code", "inlineCode", "html", "definition", "image"]);

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
    if ((node.type === "link" || node.type === "linkReference") && start !== undefined && end !== undefined) {
      const labelStart = node.children?.[0]?.position?.start.offset;
      const labelEnd = node.children?.at(-1)?.position?.end.offset;
      if (markdown[start] !== "[" || labelStart === undefined || labelEnd === undefined) {
        ranges.push([start, end]);
      } else {
        ranges.push([start, labelStart]);
        node.children?.forEach(collect);
        ranges.push([labelEnd, end]);
      }
      return;
    }
    node.children?.forEach(collect);
  }
  collect(parser.parse(markdown));

  let cursor = 0;
  const parts: string[] = [];
  for (const [start, end] of ranges) {
    parts.push(normalizeProse(markdown, cursor, start), markdown.slice(start, end));
    cursor = end;
  }
  parts.push(normalizeProse(markdown, cursor, markdown.length));
  return parts.join("");
}

function normalizeProse(markdown: string, start: number, end: number): string {
  // Consume existing dollar math and escaped backslashes before LaTeX delimiters.
  return markdown.slice(start, end).replace(
    /\$\$[\s\S]*?\$\$|\\\((?:\\[\s\S]|[^\\])*?\\\)|\\\[(?:\\[\s\S]|[^\\])*?\\\]|\\[\s\S]/g,
    (match, offset: number) => {
      if (!match.startsWith("\\(") && !match.startsWith("\\[")) return match;
      if (match.length === 2) return match;
      const body = match.slice(2, -2);
      if (!body.trim() || /\r?\n\s*\r?\n/.test(body)) return match;
      // Double dollars also delimit inline math in the existing math plugin;
      // keeping single-dollar parsing disabled avoids interpreting prices as math.
      if (match.startsWith("\\(")) return `$$${body.replace(/\r?\n/g, " ")}$$`;
      // Keep existing newlines and their Markdown container prefixes verbatim.
      if (/\r?\n/.test(body)) return `$$${body}$$`;
      const absoluteStart = start + offset;
      const absoluteEnd = absoluteStart + match.length;
      const lineStart = markdown.lastIndexOf("\n", absoluteStart - 1) + 1;
      const nextLine = markdown.indexOf("\n", absoluteEnd);
      const prefix = markdown.slice(lineStart, absoluteStart);
      const suffix = markdown.slice(absoluteEnd, nextLine < 0 ? markdown.length : nextLine);
      if (/^[\t >]*(?:(?:[-+*]|\d+[.)]) +)?$/.test(prefix) && !suffix.trim()) {
        const continuation = prefix.replace(/(?:[-+*]|\d+[.)]) +$/, marker => " ".repeat(marker.length));
        return `$$\n${continuation}${body}\n${continuation}$$`;
      }
      // Inline containers (such as table cells and links) cannot contain new rows.
      return `$$\\displaystyle ${body}$$`;
    },
  );
}
