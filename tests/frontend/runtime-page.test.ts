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
  const groupRow = findFunction(catalogSourceFile, "RuntimeToolGroupRow");

  assert.ok(pageElements.includes("RuntimeToolGroups"));
  assert.ok(pageElements.includes("RuntimeToolList"));
  assert.ok(groupsFunction, "missing RuntimeToolGroups export");
  assert.ok(groupRow, "missing RuntimeToolGroupRow");
  assert.ok(
    jsxElementNames(groupRow!, "RuntimeToolCatalog.tsx").includes(
      "RuntimeToolList",
    ),
    "expected builtin groups to render the shared RuntimeToolList",
  );
});

test("Runtime tool disclosures lazily mount each nested body", () => {
  const sourceFile = parseSource(
    runtimeToolCatalogSource,
    "RuntimeToolCatalog.tsx",
  );
  const groupRow = requireFunction(sourceFile, "RuntimeToolGroupRow");
  const toolRow = requireFunction(sourceFile, "RuntimeToolRow");
  const toolDetails = requireFunction(sourceFile, "RuntimeToolDetails");
  const rawSchema = requireFunction(sourceFile, "RuntimeToolSchema");
  const rawSchemaBody = requireFunction(sourceFile, "RuntimeToolSchemaBody");

  assert.match(groupRow.getText(), /onToggle=/);
  assert.match(groupRow.getText(), /groupOpen\s*&&[\s\S]*<RuntimeToolList/);
  assert.match(toolRow.getText(), /onToggle=/);
  assert.match(toolRow.getText(), /toolOpen\s*&&[\s\S]*<RuntimeToolDetails/);
  assert.doesNotMatch(toolRow.getText(), /runtimeToolParameters/);
  assert.match(toolDetails.getText(), /runtimeToolParameters/);
  assert.match(rawSchema.getText(), /onToggle=/);
  assert.match(
    rawSchema.getText(),
    /schemaOpen\s*&&[\s\S]*<RuntimeToolSchemaBody/,
  );
  assert.doesNotMatch(rawSchema.getText(), /formatRuntimeToolSchema/);
  assert.match(rawSchemaBody.getText(), /formatRuntimeToolSchema/);
});

test("Runtime parameter table exposes a caption and scoped column headers", () => {
  assert.match(
    runtimeToolCatalogSource,
    /<caption className="sr-only">[\s\S]*Parameters for \{tool\.name\}/,
  );
  assert.equal(
    runtimeToolCatalogSource.match(/<th scope="col"/g)?.length,
    4,
  );
});

test("MCP card keeps selectable command and error metadata outside its summary", () => {
  const sourceFile = parseSource(runtimePageSource, "Runtime.tsx");
  const card = requireFunction(sourceFile, "MCPServerCard");
  const summaries = jsxElements(card, "summary");

  assert.equal(summaries.length, 1);
  const summaryText = collectJsxText(summaries[0]).replace(/\s+/g, " ").trim();
  assert.doesNotMatch(summaryText, /\b(?:Command|Error)\b/);
  assert.match(card.getText(), />Command</);
  assert.match(card.getText(), />Error</);
  assert.match(card.getText(), /onToggle=/);
  assert.match(card.getText(), /serverOpen\s*&&[\s\S]*<RuntimeToolList/);
  assert.match(card.getText(), /server\.tools !== undefined/);
  assert.match(
    runtimePageSource,
    /Tool details unavailable in this response/,
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

function requireFunction(sourceFile: any, name: string): any {
  const fn = findFunction(sourceFile, name);
  assert.ok(fn, `missing function: ${name}`);
  return fn;
}

function jsxElements(rootNode: any, tagName: string): any[] {
  const elements: any[] = [];
  const visit = (node: any) => {
    if (
      ts.isJsxElement(node) &&
      node.openingElement.tagName.getText(node.getSourceFile()) === tagName
    ) {
      elements.push(node);
    }
    ts.forEachChild(node, visit);
  };
  visit(rootNode);
  return elements;
}

function collectJsxText(node: any): string {
  if (ts.isJsxText(node)) return node.getText();

  let text = "";
  ts.forEachChild(node, (child: any) => {
    text += collectJsxText(child);
  });
  return text;
}
