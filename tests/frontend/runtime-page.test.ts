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

test("Runtime page renders service start time from the runtime API", () => {
  assert.match(runtimePageSource, /formatRuntimeTimestamp\(data\.start_time\)/);
});

test("System Prompt and MCP use semantic tables with disclosure columns", () => {
  const sourceFile = parseSource(runtimePageSource, "Runtime.tsx");
  const systemPromptTable = requireFunction(sourceFile, "SystemPromptTable");
  const mcpTable = requireFunction(sourceFile, "MCPServerTable");
  const systemPromptRow = requireFunction(
    sourceFile,
    "SystemPromptEntryRow",
  );
  const mcpRow = requireFunction(sourceFile, "MCPServerRow");

  for (const [name, fn] of [
    ["SystemPromptTable", systemPromptTable],
    ["MCPServerTable", mcpTable],
  ] as const) {
    assert.ok(
      jsxElementNames(fn, "Runtime.tsx").includes("table"),
      `${name} must render a semantic table`,
    );
    assert.ok(
      jsxElementNames(fn, "Runtime.tsx").includes("thead"),
      `${name} must render column headers`,
    );
    assert.ok(
      jsxElementNames(fn, "Runtime.tsx").includes("tbody"),
      `${name} must render a table body`,
    );
  }

  for (const [name, fn] of [
    ["SystemPromptEntryRow", systemPromptRow],
    ["MCPServerRow", mcpRow],
  ] as const) {
    assert.ok(
      jsxElementNames(fn, "Runtime.tsx").includes("RuntimeDisclosureButton"),
      `${name} must use the shared disclosure control`,
    );
  }
});

test("Builtin tool groups and tool lists use semantic tables", () => {
  const sourceFile = parseSource(
    runtimeToolCatalogSource,
    "RuntimeToolCatalog.tsx",
  );

  for (const name of ["RuntimeToolGroups", "RuntimeToolList"] as const) {
    const fn = requireFunction(sourceFile, name);
    const elements = jsxElementNames(fn, "RuntimeToolCatalog.tsx");
    assert.ok(elements.includes("table"), `${name} must render a semantic table`);
    assert.ok(elements.includes("thead"), `${name} must render column headers`);
    assert.ok(elements.includes("tbody"), `${name} must render a table body`);
  }
});

test("Runtime disclosures share one left-chevron button component", () => {
  assert.match(
    runtimePageSource,
    /import\s+\{\s*RuntimeDisclosureButton\s*\}/,
  );
  assert.match(
    runtimeToolCatalogSource,
    /import\s+\{\s*RuntimeDisclosureButton\s*\}/,
  );
  assert.doesNotMatch(runtimePageSource, /&gt;/);
  assert.doesNotMatch(runtimePageSource, />\s*&gt;\s*</);
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

  assert.ok(
    jsxElementNames(groupRow, "RuntimeToolCatalog.tsx").includes(
      "RuntimeDisclosureButton",
    ),
  );
  assert.match(groupRow.getText(), /groupOpen\s*&&[\s\S]*<RuntimeToolList/);
  assert.ok(
    jsxElementNames(toolRow, "RuntimeToolCatalog.tsx").includes(
      "RuntimeDisclosureButton",
    ),
  );
  assert.match(toolRow.getText(), /toolOpen\s*&&[\s\S]*<RuntimeToolDetails/);
  assert.doesNotMatch(toolRow.getText(), /runtimeToolParameters/);
  assert.match(toolDetails.getText(), /runtimeToolParameters/);
  assert.ok(
    jsxElementNames(rawSchema, "RuntimeToolCatalog.tsx").includes(
      "RuntimeDisclosureButton",
    ),
  );
  assert.match(
    rawSchema.getText(),
    /schemaOpen\s*&&[\s\S]*<RuntimeToolSchemaBody/,
  );
  assert.doesNotMatch(rawSchema.getText(), /formatRuntimeToolSchema/);
  assert.match(rawSchemaBody.getText(), /formatRuntimeToolSchema/);
});

test("Runtime parameter table exposes a caption and scoped column headers", () => {
  const sourceFile = parseSource(
    runtimeToolCatalogSource,
    "RuntimeToolCatalog.tsx",
  );
  const toolDetails = requireFunction(sourceFile, "RuntimeToolDetails");

  assert.match(
    toolDetails.getText(),
    /<caption className="sr-only">[\s\S]*Parameters for \{tool\.name\}/,
  );
  assert.equal(toolDetails.getText().match(/<th scope="col"/g)?.length, 4);
});

test("MCP table row keeps command and error visible and lazily mounts tools", () => {
  const sourceFile = parseSource(runtimePageSource, "Runtime.tsx");
  const row = requireFunction(sourceFile, "MCPServerRow");

  assert.match(row.getText(), /mcpServerCommand\(server\.command/);
  assert.match(row.getText(), /server\.error/);
  assert.ok(
    jsxElementNames(row, "Runtime.tsx").includes("RuntimeDisclosureButton"),
  );
  assert.match(row.getText(), /serverOpen\s*&&[\s\S]*<RuntimeToolList/);
  assert.match(row.getText(), /server\.tools !== undefined/);
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

function collectJsxText(node: any): string {
  if (ts.isJsxText(node)) return node.getText();

  let text = "";
  ts.forEachChild(node, (child: any) => {
    text += collectJsxText(child);
  });
  return text;
}
