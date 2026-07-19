import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

const runtimeSource = source("../../frontend/src/pages/Runtime.tsx");
const promptInputSource = source(
  "../../frontend/src/components/ai-elements/prompt-input.tsx",
);
const shellSource = source("../../frontend/src/components/AppShell.tsx");
const sessionsSource = source("../../frontend/src/pages/Sessions.tsx");
const observablesSource = source("../../frontend/src/pages/Observables.tsx");
const observableDetailSource = source(
  "../../frontend/src/pages/ObservableDetail.tsx",
);
const configSource = source("../../frontend/src/pages/AgentConfig.tsx");
const conversationSource = source(
  "../../frontend/src/components/ai-elements/conversation.tsx",
);

test("runtime and sessions expose initial request failures", () => {
  assert.match(
    runtimeSource,
    /if \(error && !data\)[\s\S]*role="alert"[\s\S]*if \(!data\)/,
  );
  assert.match(sessionsSource, /setError\(/);
  assert.match(sessionsSource, /role="alert"/);
  assert.match(sessionsSource, /if \(error && !data\)/);
  assert.match(
    sessionsSource,
    /setCheckingSession\(true\);\s*setData\(null\);\s*setError\(null\)/,
    "switching agents must discard the previous agent's session-list authority",
  );
});

test("failed uncontrolled prompt submission preserves text for retry", () => {
  const resetIndex = promptInputSource.indexOf("form.reset()");
  const submitIndex = promptInputSource.indexOf(
    "const result = onSubmit({ files: convertedFiles, text }, event)",
  );
  const successIndex = promptInputSource.indexOf("await result");
  assert.ok(submitIndex >= 0 && successIndex > submitIndex);
  assert.ok(
    resetIndex > successIndex,
    "the uncontrolled form may only reset after asynchronous submit succeeds",
  );
  assert.match(promptInputSource, /submittingRef\.current/);
  assert.match(promptInputSource, /field\.value !== submittedText/);
  assert.match(promptInputSource, /clearSubmittedAttachments\(submittedFileIDs\)/);
});

test("fleet initial load failure remains an error instead of an empty fleet", () => {
  assert.match(shellSource, /setAgentsLoaded\(true\)/);
  assert.doesNotMatch(
    shellSource,
    /finally\s*\{\s*setAgentsLoaded\(true\)/,
    "only a successful roster response establishes an authoritative fleet",
  );
  assert.match(shellSource, /fleetError && !agentsLoaded/);
});

test("quiet observable polling preserves action errors", () => {
  for (const contents of [observablesSource, observableDetailSource]) {
    assert.match(contents, /if \(!quiet\) \{\s*setRefreshing\(true\);\s*setError\(null\)/);
    assert.doesNotMatch(contents, /if \(!quiet\) setRefreshing\(true\);\s*setError\(null\)/);
  }
});

test("dirty agent config is guarded across reload and in-app navigation", () => {
  assert.match(configSource, /beforeunload/);
  assert.match(configSource, /useBlocker/);
  assert.match(configSource, /Discard unsaved changes/);
});

test("conversation scroll control has an accessible name", () => {
  assert.match(conversationSource, /aria-label="Scroll to latest message"/);
});
