import assert from "node:assert/strict";
import test from "node:test";
import {
  formatShellUpdatedAt,
  shellMCPBadge,
  shellUpdatedAtClassName,
} from "../../frontend/src/lib/shell-header.ts";

test("formatShellUpdatedAt uses local date and 24-hour time without a prefix", () => {
  assert.equal(
    formatShellUpdatedAt("2026-06-01T14:18:37Z", "en-US", {
      timeZone: "Asia/Shanghai",
    }),
    "6/1/2026, 22:18:37",
  );
});

test("shellMCPBadge summarizes configured MCP servers with status dots", () => {
  assert.deepEqual(shellMCPBadge({ configured: 0, connected: 0, errors: 0 }), {
    label: "MCP 0",
    tone: "none",
    title: "No MCP servers configured",
  });
  assert.deepEqual(shellMCPBadge({ configured: 2, connected: 2, errors: 0 }), {
    label: "MCP 2",
    tone: "ok",
    title: "MCP 2/2 connected",
  });
  assert.deepEqual(shellMCPBadge({ configured: 2, connected: 1, errors: 1 }), {
    label: "MCP 2",
    tone: "error",
    title: "MCP 1/2 connected, 1 error",
  });
});

test("shellUpdatedAtClassName hides the timestamp on narrower screens", () => {
  assert.match(shellUpdatedAtClassName(), /\bhidden\b/);
  assert.match(shellUpdatedAtClassName(), /\bxl:inline\b/);
});
