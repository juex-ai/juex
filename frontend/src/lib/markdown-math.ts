import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import { unified } from "unified";

const parser = unified().use(remarkParse).use(remarkGfm);
type MarkdownNode = {
  type: string;
  referenceType?: string;
  position?: { start: { offset?: number }; end: { offset?: number } };
  children?: MarkdownNode[];
};
const literalTypes = new Set(["code", "inlineCode", "html", "definition", "image", "imageReference"]);
const proseTypes = new Set(["paragraph", "heading", "tableCell"]);

// Normalize before Streamdown splits blocks or CommonMark consumes backslashes.
// Parser offsets protect code (including nested fences), HTML, and link targets.
export function normalizeMathDelimiters(markdown: string): string {
  if (!/\\[([]/.test(markdown)) return markdown;
  const ranges: [number, number, string?][] = [];
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
        const reference = node.type === "linkReference" && node.referenceType !== "full"
          ? `][${markdown.slice(labelStart, labelEnd)}]`
          : undefined;
        ranges.push([labelEnd, end, reference]);
      }
      return;
    }
    // A closing delimiter in another block cannot finish this block's formula.
    const prose = proseTypes.has(node.type) && start !== undefined && end !== undefined;
    if (prose) ranges.push([start, start]);
    node.children?.forEach(collect);
    if (prose) ranges.push([end, end]);
  }
  collect(parser.parse(markdown));

  let cursor = 0;
  const parts: string[] = [];
  for (const [start, end, literal] of ranges) {
    parts.push(normalizeProse(markdown, cursor, start), literal ?? markdown.slice(start, end));
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
      const absoluteStart = start + offset;
      const absoluteEnd = absoluteStart + match.length;
      const lineStart = markdown.lastIndexOf("\n", absoluteStart - 1) + 1;
      const prefix = markdown.slice(lineStart, absoluteStart);
      const containers = prefix.match(/^(?:[\t ]*(?:>|[-+*]|\d+[.)])[\t ]*)*/)?.[0] ?? "";
      const depth = containers.split(">").length - 1;
      const continuation = new RegExp(`\\r?\\n(?:[\\t ]*>[\\t ]?){0,${depth}}`, "g");
      const proseBody = body.replace(continuation, "\n");
      // A quote-only blank line still separates Markdown paragraphs.
      if (!proseBody.trim() || /\n\s*\n/.test(proseBody)) return match;
      // Double dollars also delimit inline math in the existing math plugin;
      // keeping single-dollar parsing disabled avoids interpreting prices as math.
      if (match.startsWith("\\(")) {
        return `$$${proseBody.replace(/\n/g, " ")}$$`;
      }
      // Keep existing newlines and their Markdown container prefixes verbatim.
      if (/\r?\n/.test(body)) return `$$${body}$$`;
      const nextLine = markdown.indexOf("\n", absoluteEnd);
      const suffix = markdown.slice(absoluteEnd, nextLine < 0 ? markdown.length : nextLine);
      if (/^(?:[\t ]*(?:>[\t ]?|(?:[-+*]|\d+[.)])[\t ]+))*[\t ]*$/.test(prefix) && !suffix.trim()) {
        const continuation = prefix.replace(/(?:[-+*]|\d+[.)])[\t ]+/g, marker => " ".repeat(marker.length));
        return `$$\n${continuation}${body}\n${continuation}$$`;
      }
      // Inline containers (such as table cells and links) cannot contain new rows.
      return `$$\\displaystyle ${body}$$`;
    },
  );
}
