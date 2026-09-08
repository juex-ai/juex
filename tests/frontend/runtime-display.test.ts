import test from "node:test";
import assert from "node:assert/strict";

import type { MCPServerInfo, NotesSnapshot } from "../../frontend/src/types.ts";

import {
  formatRuntimeTokenCount,
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeHookCommandLabel,
  runtimeHooksSummaryLabel,
  runtimeMCPConnectionLabel,
  safeRuntimeExternalURL,
  runtimeTokenUsageDetailLabel,
} from "../../frontend/src/lib/runtime-display.ts";

test("safeRuntimeExternalURL rejects redacted and unsafe requirement links", () => {
  assert.equal(safeRuntimeExternalURL("https://example.com/docs"), "https://example.com/docs");
  assert.equal(safeRuntimeExternalURL("http://localhost:8080/install"), "http://localhost:8080/install");
  for (const value of [
    "[REDACTED_ENV]/docs",
    "https://example.com/[REDACTED_ENV]",
    "https://example.com/docs?token=[REDACTED_ENV]",
    "/relative",
    "javascript:alert(1)",
    "file:///tmp/install",
    " https://example.com/docs",
    "",
    null,
  ]) {
    assert.equal(safeRuntimeExternalURL(value), null);
  }
});

test("runtimeMCPConnectionLabel uses explicit transport metadata", () => {
  const base: MCPServerInfo = {
    name: "server",
    source: "project",
    type: "stdio",
    command: "node",
    args: ["server.js"],
    status: "connected",
    connected: true,
    tool_count: 1,
  };
  assert.equal(runtimeMCPConnectionLabel(base), "node server.js");
  assert.equal(
    runtimeMCPConnectionLabel({
      ...base,
      type: "http",
      command: "must-not-render",
      args: ["ignored"],
      url: "https://mcp.example.com/mcp",
    }),
    "https://mcp.example.com/mcp",
  );
  assert.equal(
    runtimeMCPConnectionLabel({ ...base, type: "unknown" } as MCPServerInfo),
    "-",
  );
});

test("formatRuntimeTokenCount keeps sub-thousand counts exact", () => {
  assert.equal(formatRuntimeTokenCount(999), "999");
});

test("formatRuntimeTokenCount formats large counts without 1000k", () => {
  assert.equal(formatRuntimeTokenCount(999_950), "1m");
  assert.equal(formatRuntimeTokenCount(1_250_000), "1.3m");
});

test("runtimeContextPercentLabel summarizes context window usage", () => {
  assert.equal(runtimeContextPercentLabel(undefined), "-");
  assert.equal(
    runtimeContextPercentLabel({
      context_window: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 42,
    }),
    "-",
  );
  assert.equal(
    runtimeContextPercentLabel({
      context_window: 10_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 5_950,
    }),
    "~59.5%",
  );
  assert.equal(
    runtimeContextPercentLabel({
      context_window: 10_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
    }),
    "~0%",
  );
});

test("runtime context tooltip labels separate model, window, and total tokens", () => {
  assert.equal(
    runtimeContextModelLabel({
      model: " clip-local:gemini-3.1-pro-low ",
      context_window: 256_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 156_800,
    }),
    "clip-local:gemini-3.1-pro-low",
  );
  assert.equal(
    runtimeContextWindowDetailLabel({
      model: "clip-local:gemini-3.1-pro-low",
      context_window: 256_000,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 156_800,
    }),
    "context window: ~156.8k/256k tokens (~61.3%)",
  );
  assert.equal(
    runtimeTokenUsageDetailLabel({
      input_tokens: 22_900_000,
      output_tokens: 39_300,
    }),
    "total tokens: 22.9m in / 39.3k out",
  );
  assert.equal(runtimeContextModelLabel(undefined), "unknown");
  assert.equal(
    runtimeContextWindowDetailLabel(undefined),
    "context window: 0/0 tokens (0%)",
  );
});

test("runtimeHooksSummaryLabel pluralizes configured hooks", () => {
  assert.equal(runtimeHooksSummaryLabel({ configured: 0, commands: [] }), "0 hooks");
  assert.equal(
    runtimeHooksSummaryLabel({
      configured: 1,
      commands: [
        {
          name: "guard",
          events: ["PreToolUse"],
          command: ["python3", "guard.py"],
          required: false,
          timeout_seconds: 10,
          max_output_bytes: 65536,
        },
      ],
    }),
    "1 hook",
  );
});

test("runtimeHookCommandLabel joins command argv", () => {
  assert.equal(runtimeHookCommandLabel(["python3", "guard.py"]), "python3 guard.py");
  assert.equal(runtimeHookCommandLabel([]), "-");
});
