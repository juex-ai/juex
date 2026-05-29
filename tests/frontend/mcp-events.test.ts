import assert from "node:assert/strict";
import test from "node:test";
import {
  formatMCPEventForDisplay,
  oneLinePreview,
  parseMCPEventText,
} from "../../frontend/src/lib/mcp-events.ts";

test("parseMCPEventText extracts source and event type as the label", () => {
  assert.deepEqual(parseMCPEventText("eigenflux:pm_update:{\"id\":\"42\"}"), {
    label: "eigenflux:pm_update",
    content: "{\"id\":\"42\"}",
  });
});

test("parseMCPEventText keeps the raw text under a fallback label", () => {
  assert.deepEqual(parseMCPEventText("raw notification"), {
    label: "mcp:event",
    content: "raw notification",
  });
});

test("parseMCPEventText keeps raw text when label segments are empty", () => {
  assert.deepEqual(parseMCPEventText(":pm_update:{\"id\":\"42\"}"), {
    label: "mcp:event",
    content: ":pm_update:{\"id\":\"42\"}",
  });
  assert.deepEqual(parseMCPEventText("eigenflux::{\"id\":\"42\"}"), {
    label: "mcp:event",
    content: "eigenflux::{\"id\":\"42\"}",
  });
});

test("oneLinePreview collapses multiline event content into one row", () => {
  assert.equal(
    oneLinePreview("first line\n\n  second\tline  "),
    "first line second line",
  );
});

test("oneLinePreview truncates long content", () => {
  assert.equal(oneLinePreview("a".repeat(150)), `${"a".repeat(120)}...`);
});

test("formatMCPEventForDisplay returns a collapsed preview", () => {
  assert.deepEqual(formatMCPEventForDisplay("server:changed:line 1\nline 2"), {
    label: "server:changed",
    content: "line 1\nline 2",
    preview: "line 1 line 2",
  });
});
