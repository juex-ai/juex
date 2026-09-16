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

test("preserves blockquote and list prefixes around display math", () => {
  for (const prefix of ["> ", "> > ", "  "]) {
    const lead = prefix === "  " ? "- " : prefix;
    const source = `${lead}\\[\n${prefix}x_1 + x_2\n${prefix}\\]`;
    assert.equal(normalizeMathDelimiters(source), `${lead}$$\n${prefix}x_1 + x_2\n${prefix}$$`);
    assert.equal(normalizeMathDelimiters(`${lead}\\[x_1 + x_2\\]`), `${lead}$$\n${prefix}x_1 + x_2\n${prefix}$$`);
  }
});

test("keeps display math inside a table cell or prose on one line", () => {
  assert.equal(normalizeMathDelimiters(String.raw`| 值 | \[\frac f3\] |`), String.raw`| 值 | $$\displaystyle \frac f3$$ |`);
  assert.equal(normalizeMathDelimiters(String.raw`前文 \[x\] 后文`), String.raw`前文 $$\displaystyle x$$ 后文`);
});

test("normalizes link labels while preserving destinations, titles, and references", () => {
  assert.equal(normalizeMathDelimiters(String.raw`[value \(x\)](https://example.com/\(path\) "\(title\)")`), String.raw`[value $$x$$](https://example.com/\(path\) "\(title\)")`);
  assert.equal(normalizeMathDelimiters(String.raw`[\[x\]](https://example.com)`), String.raw`[$$\displaystyle x$$](https://example.com)`);
  assert.equal(normalizeMathDelimiters(String.raw`[\(x\)][ref]

[ref]: https://example.com/\(path\)`), String.raw`[$$x$$][ref]

[ref]: https://example.com/\(path\)`);
});

test("protects bare GFM URLs and reference images", () => {
  const source = String.raw`https://example.com/?expr=\(x\)

![\(x\)]

[\(x\)]: /plot.png`;
  assert.equal(normalizeMathDelimiters(source), source);
});

test("shortcut and collapsed links retain the original reference key", () => {
  for (const suffix of ["", "[]"]) {
    const definition = String.raw`[\(x\)]: https://example.com/target`;
    assert.equal(normalizeMathDelimiters(`[\\(x\\)]${suffix}\n\n${definition}`), `[$$x$$][\\(x\\)]\n\n${definition}`);
  }
});

test("multiline inline math strips only the quote's continuation markers", () => {
  assert.equal(normalizeMathDelimiters(String.raw`> \(a +
> b\)`), "> $$a + b$$");
  assert.equal(normalizeMathDelimiters(String.raw`> > \(a
> > > b\)`), "> > $$a > b$$");
  assert.equal(normalizeMathDelimiters(String.raw`> \(a +
b\)`), "> $$a + b$$");
});
