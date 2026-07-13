import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const apiSource = readFileSync(
  new URL("../../frontend/src/api.ts", import.meta.url),
  "utf8",
);
const sessionSource = readFileSync(
  new URL("../../frontend/src/pages/Session.tsx", import.meta.url),
  "utf8",
);
const fileTreeSource = readFileSync(
  new URL("../../frontend/src/components/FileTreePanel.tsx", import.meta.url),
  "utf8",
);

test("session scratchpad uses a session-scoped tree API", () => {
  assert.match(apiSource, /export async function getSessionScratchpad/);
  assert.match(apiSource, /api\/sessions\/\$\{encodeURIComponent\(id\)\}\/scratchpad/);
});

test("active and read-only sessions expose the scratchpad browser", () => {
  assert.match(sessionSource, /function ScratchpadButton/);
  assert.match(sessionSource, /<ScratchpadButton sessionID=\{data\.id\} \/>/);
  assert.match(sessionSource, /SessionRuntimeStateBadges[\s\S]*ScratchpadButton/);
  assert.match(sessionSource, /ReadOnlySessionBar[\s\S]*ScratchpadButton/);
  assert.match(sessionSource, /aria-label="Browse session scratchpad"/);
});

test("file tree panel supports a scoped loader and empty state", () => {
  assert.match(fileTreeSource, /loadTree = getFileTree/);
  assert.match(fileTreeSource, /emptyLabel/);
  assert.match(fileTreeSource, /title = "Workspace"/);
});
