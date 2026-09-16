import assert from "node:assert/strict";
import test from "node:test";
import { normalizeMathDelimiters } from "../../frontend/src/lib/markdown-math.ts";

test("normalizes inline and multiline display LaTeX without changing the formula", () => {
  assert.equal(normalizeMathDelimiters(String.raw`这里的 \(f_3\) 是次焦距。
\[
\boxed{f_3=\frac{b^2}{3\lambda}
=\frac f3=0.1316898\ \mathrm m}.
\]`), String.raw`这里的 $$f_3$$ 是次焦距。
$$
\boxed{f_3=\frac{b^2}{3\lambda}
=\frac f3=0.1316898\ \mathrm m}.
$$`);
});

test("normalizes formulas in headings, lists, and table cells", () => {
  assert.equal(normalizeMathDelimiters(String.raw`### 物距 \(f/2\)
- \(x_1+x_2\)
| 值 | \(\lvert J\rvert\) |
|---|---|`), String.raw`### 物距 $$f/2$$
- $$x_1+x_2$$
| 值 | $$\lvert J\rvert$$ |
|---|---|`);
});

test("keeps fenced, indented, inline, and nested code literal", () => {
  const code = String.raw`\(x\)`;
  const markdown = [
    `Inline \`${code}\` and \`\` ${code} \` literal \`\``,
    "", "```tex", code, "```", "", "~~~tex", code, "~~~", "",
    `    ${code}`, "", "> ```tex", `> ${code}`, "> ```", "",
    "- Example:", "", "  ```tex", `  ${code}`, "  ```", "",
  ].join("\n");
  assert.equal(normalizeMathDelimiters(markdown), markdown);
  assert.equal(normalizeMathDelimiters(`${markdown}\n${code}`), `${markdown}\n$$x$$`);
});

test("preserves escaped delimiters, existing math, currency, links, and HTML", () => {
  const markdown = String.raw`\\(literal\\) \\[literal\\] $$\text{\(literal\)}$$
Price: $5 and $10. [link](https://example.com/\(x\))
<pre>\(literal\)</pre>`;
  assert.equal(normalizeMathDelimiters(markdown), markdown);
});

test("leaves incomplete streaming formulas alone until their delimiter closes", () => {
  const partial = String.raw`结果 \(\frac{a_1}{b}`;
  assert.equal(normalizeMathDelimiters(partial), partial);
  assert.equal(normalizeMathDelimiters(partial + String.raw`\)`), String.raw`结果 $$\frac{a_1}{b}$$`);
  assert.equal(normalizeMathDelimiters(String.raw`\[a+b`), String.raw`\[a+b`);
});

test("does not match a formula across literal code or separate paragraphs", () => {
  const markdown = "\\(unclosed\n\n`literal`\n\nclosing\\)";
  assert.equal(normalizeMathDelimiters(markdown), markdown);
});
