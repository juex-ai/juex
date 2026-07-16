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
const runtimeToolCatalogSource = readFileSync(
  new URL(
    "../../frontend/src/components/RuntimeToolCatalog.tsx",
    import.meta.url,
  ),
  "utf8",
);

test("Runtime page orders Provider, Tools, MCP, Skills, and Hooks", () => {
  const headings = runtimePageHeadings(runtimePageSource);
  const expected = ["Provider", "Tools", "MCP", "Skills", "Hooks"];
  const offsets = expected.map((heading) => headings.indexOf(heading));

  expected.forEach((heading, index) => {
    assert.notEqual(offsets[index], -1, `missing Runtime heading: ${heading}`);
  });
  assert.deepEqual([...offsets].sort((a, b) => a - b), offsets);
});

test("Runtime page shares RuntimeToolList across builtin and MCP paths", () => {
  const pageElements = jsxElementNames(runtimePageSource, "Runtime.tsx");
  const catalogSourceFile = parseSource(
    runtimeToolCatalogSource,
    "RuntimeToolCatalog.tsx",
  );
  const groupsFunction = findFunction(catalogSourceFile, "RuntimeToolGroups");

  assert.ok(pageElements.includes("RuntimeToolGroups"));
  assert.ok(pageElements.includes("RuntimeToolList"));
  assert.ok(groupsFunction, "missing RuntimeToolGroups export");
  assert.ok(
    jsxElementNames(groupsFunction!, "RuntimeToolCatalog.tsx").includes(
      "RuntimeToolList",
    ),
    "expected builtin groups to render the shared RuntimeToolList",
  );
});

function runtimePageHeadings(source: string): string[] {
  const sourceFile = parseSource(source, "Runtime.tsx");
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

function parseSource(source: string, filename: string): any {
  return ts.createSourceFile(
    filename,
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
}

function jsxElementNames(source: string | any, filename: string): string[] {
  const rootNode = typeof source === "string" ? parseSource(source, filename) : source;
  const sourceFile = rootNode.getSourceFile();
  const names: string[] = [];
  const visit = (node: any) => {
    if (ts.isJsxElement(node)) {
      names.push(node.openingElement.tagName.getText(sourceFile));
    } else if (ts.isJsxSelfClosingElement(node)) {
      names.push(node.tagName.getText(sourceFile));
    }
    ts.forEachChild(node, visit);
  };
  visit(rootNode);
  return names;
}

function findFunction(sourceFile: any, name: string): any | undefined {
  return sourceFile.statements.find(
    (statement: any) =>
      ts.isFunctionDeclaration(statement) && statement.name?.text === name,
  );
}

function collectJsxText(node: any): string {
  if (ts.isJsxText(node)) return node.getText();

  let text = "";
  ts.forEachChild(node, (child: any) => {
    text += collectJsxText(child);
  });
  return text;
}
