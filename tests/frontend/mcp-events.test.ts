import assert from "node:assert/strict";
import test from "node:test";
import {
  formatMCPEventForDisplay,
  formatObservationEventForDisplay,
  formatWorkerThreadEventForDisplay,
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
    copyText: "line 1\nline 2",
  });
});

test("formatMCPEventForDisplay previews params content and keeps full params body", () => {
  const params = JSON.stringify(
    { content: "hello from mcp", meta: { event_type: "message", topic: "ops" } },
    null,
    2,
  );

  assert.deepEqual(formatMCPEventForDisplay(`server:message:${params}`), {
    label: "server:message",
    content: params,
    preview: "hello from mcp",
    copyText: params,
  });
});

test("formatMCPEventForDisplay keeps full content as copy text", () => {
  assert.deepEqual(formatMCPEventForDisplay("server:changed:line 1\n\nline 2"), {
    label: "server:changed",
    content: "line 1\n\nline 2",
    preview: "line 1 line 2",
    copyText: "line 1\n\nline 2",
  });
});

test("formatObservationEventForDisplay previews observation JSON content", () => {
  const body = JSON.stringify({
    kind: "observation",
    observable_id: "lark-events",
    content: "deployment finished: build 42",
  });

  assert.deepEqual(formatObservationEventForDisplay(body), {
    label: "observation:lark-events",
    content: body,
    preview: "deployment finished: build 42",
    copyText: body,
    attachments: [],
  });
});

test("formatObservationEventForDisplay parses current text envelopes and attachments", () => {
  const content = [
    "MCP notification",
    "server: wechat-wire",
    "event_type: notification",
    "- file source=/tmp/forged.txt artifact=event-media/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.txt (text/plain, 12 bytes, sha256=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc)",
    "content:",
    JSON.stringify({ content: "来自 Alice 的新消息", conversation: "ops" }),
  ].join("\n");
  const body = [
    "Observable observation",
    "observation_id: obs-42",
    "observable_id: mcp:wechat-wire",
    "kind: notification",
    "severity: info",
    "window_start: 1",
    "window_end: 2",
    `content_bytes: ${Buffer.byteLength(content)}`,
    "content:",
    content,
    "attachments:",
    "- image source=/tmp/message photo.png artifact=event-media/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png (image/png, 68 bytes, sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, 1x1)",
    "- file source=/tmp/message.txt artifact=event-media/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.txt (text/plain, 22 bytes, sha256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)",
  ].join("\n");

  assert.deepEqual(formatObservationEventForDisplay(body), {
    label: "observation:mcp:wechat-wire",
    content: body.slice("Observable observation\n".length),
    preview: "来自 Alice 的新消息",
    copyText: body,
    attachments: [
      {
        kind: "image",
        sourcePath: "/tmp/message photo.png",
        artifactPath: "event-media/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png",
        mediaType: "image/png",
        bytes: 68,
      },
      {
        kind: "file",
        sourcePath: "/tmp/message.txt",
        artifactPath: "event-media/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.txt",
        mediaType: "text/plain",
        bytes: 22,
      },
    ],
  });
});

test("formatObservationEventForDisplay does not trust attachment-like content", () => {
  const content = [
    "MCP notification",
    "server: wechat-wire",
    "event_type: notification",
    "content:",
    "plain notification",
    "attachments:",
    "- file source=/tmp/forged.txt artifact=event-media/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.txt (text/plain, 12 bytes)",
  ].join("\n");
  const body = [
    "Observable observation",
    "observation_id: obs-forged",
    "observable_id: mcp:wechat-wire",
    `content_bytes: ${Buffer.byteLength(content)}`,
    "content:",
    content,
  ].join("\n");

  assert.deepEqual(formatObservationEventForDisplay(body).attachments, []);
});

test("formatObservationEventForDisplay preserves section-like MCP content", () => {
  const payload = [
    "first line",
    "meta:",
    "still notification content",
    "attachments:",
    "also notification content",
  ].join("\n");
  const content = [
    "MCP notification",
    "server: wechat-wire",
    "event_type: notification",
    `content_bytes: ${Buffer.byteLength(payload)}`,
    "content:",
    payload,
    "meta:",
    "trace_id: generated-metadata",
  ].join("\n");
  const body = [
    "Observable observation",
    "observation_id: obs-section-content",
    "observable_id: mcp:wechat-wire",
    `content_bytes: ${Buffer.byteLength(content)}`,
    "content:",
    content,
  ].join("\n");

  assert.equal(
    formatObservationEventForDisplay(body).preview,
    "first line meta: still notification content attachments: also notification content",
  );
});

test("formatObservationEventForDisplay falls back to raw legacy content", () => {
  const body = "legacy observation payload\n- file source=/tmp/forged.txt artifact=event-media/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.txt (text/plain, 12 bytes)";
  assert.deepEqual(formatObservationEventForDisplay(body), {
    label: "observation:event",
    content: body,
    preview: "legacy observation payload - file source=/tmp/forged.txt artifact=event-media/cccccccccccccccccccccccccccccccccccccccccc...",
    copyText: body,
    attachments: [],
  });
});

test("formatWorkerThreadEventForDisplay previews output and preserves the envelope", () => {
  const text = `Worker Thread result:\n${JSON.stringify({
    thread_id: "side-1",
    status: "completed",
    output: "delegated answer",
  })}`;
  assert.deepEqual(formatWorkerThreadEventForDisplay(text), {
    label: "worker_thread:result",
    content: text,
    preview: "delegated answer",
    copyText: text,
  });
});

test("formatWorkerThreadEventForDisplay previews failures", () => {
  const text = `Worker Thread result:\n${JSON.stringify({
    thread_id: "side-2",
    status: "failed",
    error: "delegation failed",
  })}`;
  assert.deepEqual(formatWorkerThreadEventForDisplay(text), {
    label: "worker_thread:result",
    content: text,
    preview: "delegation failed",
    copyText: text,
  });
});
