import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";

const requireFromFrontend = createRequire(
  new URL("../../frontend/package.json", import.meta.url),
);
const ts = requireFromFrontend("typescript");

const runtimePageSource = readFileSync(
  new URL("../../frontend/src/pages/Runtime.tsx", import.meta.url),
  "utf8",
);

test("Runtime page places hooks after skills", () => {
  const headings = runtimePageHeadings(runtimePageSource);
  const skillsOffset = headings.indexOf("Skills");
  const hooksOffset = headings.indexOf("Hooks");

  assert.notEqual(skillsOffset, -1, "missing Runtime heading: Skills");
  assert.notEqual(hooksOffset, -1, "missing Runtime heading: Hooks");
  assert.ok(
    hooksOffset > skillsOffset,
    "expected the Runtime Hooks section to render after the Skills section",
  );
});

function runtimePageHeadings(source: string): string[] {
  const sourceFile = ts.createSourceFile(
    "Runtime.tsx",
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
  const headings: string[] = [];

  const visit = (node: any) => {
    if (
      ts.isJsxElement(node) &&
      node.openingElement.tagName.getText(sourceFile) === "h1"
    ) {
      const text = collectJsxText(node).replace(/\s+/g, " ").trim();
      if (text) headings.push(text);
    }
    ts.forEachChild(node, visit);
  };

  visit(sourceFile);
  return headings;
}

function collectJsxText(node: any): string {
  if (ts.isJsxText(node)) return node.getText();

  let text = "";
  ts.forEachChild(node, (child: any) => {
    text += collectJsxText(child);
  });
  return text;
}
