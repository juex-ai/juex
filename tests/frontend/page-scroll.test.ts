import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import test from "node:test";

const require = createRequire(
  new URL("../../frontend/package.json", import.meta.url),
);
const ts = require("typescript");

function classTokenSets(path: string): Set<string>[] {
  const source = readFileSync(new URL(path, import.meta.url), "utf8");
  const sourceFile = ts.createSourceFile(
    path,
    source,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
  const tokenSets: Set<string>[] = [];

  const collectStrings = (node: unknown) => {
    if (ts.isStringLiteralLike(node)) {
      tokenSets.push(new Set(node.text.trim().split(/\s+/).filter(Boolean)));
      return;
    }
    ts.forEachChild(node, collectStrings);
  };
  const visit = (node: unknown) => {
    if (
      ts.isJsxAttribute(node) &&
      ["className", "scrollClassName"].includes(node.name.getText(sourceFile))
    ) {
      if (node.initializer) collectStrings(node.initializer);
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(sourceFile);
  return tokenSets;
}

function assertHasClassTokens(
  tokenSets: Set<string>[],
  expected: string,
): void {
  const expectedTokens = expected.split(/\s+/).filter(Boolean);
  assert.ok(
    tokenSets.some((tokens) =>
      expectedTokens.every((token) => tokens.has(token)),
    ),
    `expected one class expression to contain: ${expected}`,
  );
}

const shellClasses = classTokenSets(
  "../../frontend/src/components/AppShell.tsx",
);
const conversationClasses = classTokenSets(
  "../../frontend/src/components/ai-elements/conversation.tsx",
);
const sessionClasses = classTokenSets("../../frontend/src/pages/Session.tsx");
const runtimeClasses = classTokenSets("../../frontend/src/pages/Runtime.tsx");

test("the app shell owns an exact clipped viewport", () => {
  assertHasClassTokens(
    shellClasses,
    "fixed inset-0 h-svh min-h-0 overflow-clip",
  );
});

test("the session keeps one explicit conversation scroller above the composer", () => {
  assertHasClassTokens(
    conversationClasses,
    "relative min-h-0 flex-1 overflow-hidden",
  );
  assertHasClassTokens(
    conversationClasses,
    "h-full min-h-0 overflow-y-auto overscroll-contain",
  );
  assertHasClassTokens(
    sessionClasses,
    "flex min-h-0 flex-1 flex-col overflow-hidden",
  );
  assertHasClassTokens(sessionClasses, "shrink-0 border-t");
});

test("runtime owns vertical scrolling without document scroll chaining", () => {
  assertHasClassTokens(
    runtimeClasses,
    "min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain",
  );
});
