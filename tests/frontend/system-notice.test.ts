import assert from "node:assert/strict";
import test from "node:test";

import { formatSystemNotice } from "../../frontend/src/lib/system-notice.ts";

test("restart system notice gets a stable title and normalized content", () => {
  const display = formatSystemNotice(
    "System notice: this agent was restarted while the previous turn was active.",
  );

  assert.equal(display.title, "Agent restarted");
  assert.equal(
    display.content,
    "this agent was restarted while the previous turn was active.",
  );
});

test("generic system notice keeps its content and uses the fallback title", () => {
  const display = formatSystemNotice("Scheduled maintenance begins soon.");

  assert.equal(display.title, "System notice");
  assert.equal(display.content, "Scheduled maintenance begins soon.");
});

test("system notice normalization is case insensitive and trims whitespace", () => {
  const display = formatSystemNotice("  SYSTEM NOTICE:   Agent restarted.  ");

  assert.equal(display.title, "Agent restarted");
  assert.equal(display.content, "Agent restarted.");
});
