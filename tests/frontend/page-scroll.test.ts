import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

const globalStyles = source("../../frontend/src/index.css");
const conversationSource = source(
  "../../frontend/src/components/ai-elements/conversation.tsx",
);
const sessionSource = source("../../frontend/src/pages/Session.tsx");
const runtimeSource = source("../../frontend/src/pages/Runtime.tsx");

test("the app root owns an exact non-scrolling viewport", () => {
  assert.match(
    globalStyles,
    /html,\s*body,\s*#root\s*\{[\s\S]*?height:\s*100%;[\s\S]*?min-height:\s*0;/,
  );
  assert.match(
    globalStyles,
    /html,\s*body\s*\{[\s\S]*?overflow:\s*hidden;/,
  );
  assert.match(
    globalStyles,
    /#root\s*\{[\s\S]*?position:\s*fixed;[\s\S]*?inset:\s*0;[\s\S]*?overflow:\s*clip;/,
  );
  assert.doesNotMatch(globalStyles, /body\s*\{[\s\S]*?min-h-svh/);
});

test("the session keeps one explicit conversation scroller above the composer", () => {
  assert.match(
    conversationSource,
    /scrollClassName=\{cn\(\s*"h-full min-h-0 overflow-y-auto overscroll-contain"/,
  );
  assert.match(
    sessionSource,
    /<div className="flex min-h-0 flex-1 flex-col overflow-hidden">/,
  );
  assert.match(
    sessionSource,
    /<div className="shrink-0 border-t bg-background\/92/,
  );
});

test("runtime owns vertical scrolling without document scroll chaining", () => {
  assert.match(
    runtimeSource,
    /className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background"/,
  );
});
