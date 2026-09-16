import { createRequire } from "node:module";
const require = createRequire(new URL("../../../frontend/package.json", import.meta.url));
const { expect, test } = require("@playwright/test");

const answer = String.raw`这里的 \(f_3\) 表示三级次焦距。

\[
\boxed{f_3=\frac{b^2}{3\lambda}
=\frac f3
=0.1316898\ \mathrm m\approx0.132\ \mathrm m}.
\]

### 物距变为 \(f/2\)

| 级次 | 值 |
|---|---|
| 次焦点 | \(\frac f3\) |

已有公式 $$E=mc^2$$。价格 $5 和 $10。

代码示例：` + "`\\(x_1\\)`\n\n```tex\n\\[\\frac{a}{b}\\]\n```";

async function openThread(page, text) {
  await page.addInitScript(() => {
    window.sources = [];
    window.EventSource = class extends EventTarget {
      static OPEN = 1; static CLOSED = 2; static CONNECTING = 0;
      readyState = 1;
      constructor(url) { super(); this.url = String(url); window.sources.push(this); }
      close() { this.readyState = 2; }
    };
  });
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    const json = body => route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/agents") return json([{ id: "math", name: "debaga", enabled: true, workspace: "/tmp/math", runtime_health: "healthy" }]);
    if (path.endsWith("/status")) return json({ cursor: "cursor-1", thread: { id: "0", state: "idle", working: false, pending_count: 0, can_accept_input: true }, tools: [], token_usage: {} });
    if (path.endsWith("/recitation")) return json(null);
    if (path.endsWith("/files/tree")) return json({ name: "workspace", path: "/", is_dir: true, children: [] });
    if (path.endsWith("/threads/0")) return json({ thread_id: "0", alias: "main", dir: "/tmp/math/0", retention_state: "active", execution_state: "idle", revision: 1, generation_id: "g1", event_cursor: "cursor-1", has_more_before: false,
      items: text ? [{ type: "message", message: { id: "answer", role: "assistant", blocks: [{ type: "text", text }] } }] : [] });
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.goto("/agents/math/threads/0");
  await expect(page.locator("header")).toContainText("main");
}

for (const width of [1440, 390]) {
  test(`physics formulas render with local fonts and literal code at ${width}px`, async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.setViewportSize({ width, height: 900 });
    await openThread(page, answer + `\n\n\\[${"a_1 + ".repeat(50)}z\\]`);
    const markdown = page.locator(".juex-markdown");
    await expect(markdown.locator(".katex")).toHaveCount(6);
    await expect(markdown.locator(".katex-display")).toHaveCount(2);
    await expect(markdown.locator(".katex-error")).toHaveCount(0);
    await expect(markdown.locator("h3 .katex")).toHaveCount(1);
    await expect(markdown.locator("td .katex")).toHaveCount(1);
    await expect(markdown.locator("code").first()).toHaveText(String.raw`\(x_1\)`);
    await expect(markdown.locator("pre")).toContainText(String.raw`\[\frac{a}{b}\]`);
    await expect(markdown).toContainText("价格 $5 和 $10。");
    const layout = await markdown.evaluate(async element => {
      await document.fonts.ready;
      const math = element.querySelector(".katex");
      const mathml = element.querySelector(".katex-mathml");
      const wide = [...element.querySelectorAll(".katex-display")].at(-1);
      return { font: getComputedStyle(math).fontFamily, mathmlPosition: getComputedStyle(mathml).position,
        fontLoaded: document.fonts.check("16px KaTeX_Main"), overflow: getComputedStyle(wide).overflowX,
        wide: wide.scrollWidth > wide.clientWidth, width: document.documentElement.scrollWidth };
    });
    expect(layout).toMatchObject({ mathmlPosition: "absolute", fontLoaded: true, overflow: "auto", wide: true, width });
    expect(layout.font).toContain("KaTeX_Main");
    expect(errors).toEqual([]);
  });
}

test("streamed LaTeX becomes a formula when the closing delimiter arrives", async ({ page }) => {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await openThread(page, "");
  const delta = text => page.evaluate(text => {
    const source = window.sources.find(source => source.readyState === 1 && source.url.includes("/threads/0/events"));
    source.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
      type: "llm.output_delta", thread_id: "0", turn_id: "turn-math", ts: "2026-09-16T08:00:00Z",
      status: { cursor: "cursor-live", thread: { id: "0", state: "turn_active", working: true, pending_count: 0, can_accept_input: true }, tools: [], token_usage: {} },
      payload: { kind: "text", index: 0, iter: 0, text },
    }) }));
  }, text);
  await delta(String.raw`结果 \(\frac{a_1}{b}`);
  await expect(page.locator(".juex-markdown")).toContainText("结果");
  await delta(String.raw`\)`);
  await expect(page.locator(".juex-markdown .katex")).toHaveCount(1);
  expect(errors).toEqual([]);
});

test("display formulas preserve quote, list, table, and link containers", async ({ page }) => {
  await openThread(page, String.raw`> \[
> \frac{a}{b}
> \]

- \[
  x_1+x_2
  \]
- Second item

| Type | Value |
|---|---|
| display | \[\frac f3\] |

[value \(x\)](https://example.com/target)

[\[y\]](https://example.com/display)`);
  const markdown = page.locator(".juex-markdown");
  await expect(markdown.locator(".katex")).toHaveCount(5);
  await expect(markdown.locator(".katex-error")).toHaveCount(0);
  await expect(markdown.locator("blockquote .katex-display")).toHaveCount(1);
  await expect(markdown.locator("li .katex-display")).toHaveCount(1);
  await expect(markdown.locator("li")).toHaveCount(2);
  await expect(markdown.locator("tbody tr")).toHaveCount(1);
  await expect(markdown.locator("td .katex")).toHaveCount(1);
  await expect(markdown.locator('[data-streamdown="link"] .katex')).toHaveCount(2);
  await markdown.locator('[data-streamdown="link"]').filter({ hasText: "value" }).click();
  await expect(page.locator('[data-streamdown="link-safety-modal"]')).toContainText("https://example.com/target");
});

test("math keeps GFM URLs intact and drops quote continuation markers", async ({ page }) => {
  await openThread(page, String.raw`https://example.com/?expr=\(x\)

> \(a +
> b\)`);
  const markdown = page.locator(".juex-markdown");
  await expect(markdown.locator('blockquote annotation')).toHaveText("a + b");
  await expect(markdown.locator('[data-streamdown="link"] .katex')).toHaveCount(0);
  const bareLink = markdown.locator('[data-streamdown="link"]').filter({ hasText: "https://example.com/?expr=" });
  await bareLink.click();
  await expect(page.locator('[data-streamdown="link-safety-modal"]')).toContainText("https://example.com/?expr=%5C(x%5C)");
});

test("unclosed quoted formulas preserve separate paragraphs", async ({ page }) => {
  await openThread(page, String.raw`> \(open
>
> close\)`);
  const markdown = page.locator(".juex-markdown");
  await expect(markdown.locator("blockquote p")).toHaveCount(2);
  await expect(markdown.locator("blockquote p").first()).toContainText("open");
  await expect(markdown.locator("blockquote p").last()).toContainText("close");
  await expect(markdown.locator(".katex")).toHaveCount(0);
});
