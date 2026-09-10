import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const fileTreeSource = readFileSync(
  new URL("../../frontend/src/components/FileTreePanel.tsx", import.meta.url),
  "utf8",
);

test("file tree panel supports a scoped loader, empty state, and header title", () => {
  assert.match(fileTreeSource, /loadTree = getFileTree/);
  assert.match(fileTreeSource, /emptyLabel/);
  assert.match(fileTreeSource, /title = "Workspace"/);
  assert.match(fileTreeSource, /headerTitle\?: ReactNode/);
  assert.match(fileTreeSource, /headerTitle \?\?/);
  assert.match(fileTreeSource, /rootKey\?: string/);
  assert.match(fileTreeSource, /useLayoutEffect\(\(\) => \{/);
});

test("file tree keeps its scroll viewport inside the remaining panel height", () => {
  assert.match(
    fileTreeSource,
    /<ScrollArea className="min-h-0 flex-1 overflow-hidden p-3">/,
  );
});
