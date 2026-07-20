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
const shellSource = readFileSync(
  new URL("../../frontend/src/components/AppShell.tsx", import.meta.url),
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

test("session controls leave scratchpad browsing to the file panel", () => {
  assert.doesNotMatch(sessionSource, /ScratchpadButton/);
  assert.doesNotMatch(sessionSource, /Browse session scratchpad/);
  assert.doesNotMatch(sessionSource, /getSessionScratchpad/);
});

test("session routes switch the shared file panel between roots", () => {
  assert.match(
    shellSource,
    /useMatch\("\/agents\/:agentId\/sessions\/:sessionId"\)/,
  );
  assert.match(shellSource, /type FilePanelMode = "workspace" \| "scratchpad"/);
  assert.match(shellSource, /getSessionScratchpad\(sessionID, signal\)/);
  assert.match(shellSource, /filePanelMode === "scratchpad"/);
  assert.match(
    shellSource,
    /route === location\.pathname[\s\S]*mode: "workspace"/,
  );
  assert.match(shellSource, /Show scratchpad/);
  assert.match(shellSource, /Show workspace/);
  assert.match(shellSource, /headerAction: filePanelHeaderAction/);
  assert.equal(
    shellSource.match(/rootKey=\{filePanelKey\}/g)?.length,
    2,
    "desktop and mobile file panels must reset for the selected root",
  );
});

test("file tree panel supports a scoped loader, empty state, and header action", () => {
  assert.match(fileTreeSource, /loadTree = getFileTree/);
  assert.match(fileTreeSource, /emptyLabel/);
  assert.match(fileTreeSource, /title = "Workspace"/);
  assert.match(fileTreeSource, /headerAction\?: ReactNode/);
  assert.match(fileTreeSource, /\{headerAction\}/);
  assert.match(fileTreeSource, /rootKey\?: string/);
  assert.match(fileTreeSource, /useLayoutEffect\(\(\) => \{/);
});
