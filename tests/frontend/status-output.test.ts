import assert from "node:assert/strict";
import test from "node:test";
import { formatStatusOutput } from "../../frontend/src/lib/status-output.ts";

test("formatStatusOutput splits status text into scan-friendly rows", () => {
  const status = formatStatusOutput(
    [
      "Juex status",
      "session: 20260530T100000-demo (3 turns)",
      "provider: openai / gpt-4.1 / https://api.example.test/v1",
      "queued input: 2/5",
    ].join("\n"),
  );

  assert.deepEqual(status, {
    title: "Juex status",
    titleIcon: "📊",
    rows: [
      {
        icon: "💬",
        label: "session",
        value: "20260530T100000-demo (3 turns)",
        raw: "session: 20260530T100000-demo (3 turns)",
      },
      {
        icon: "🤖",
        label: "provider",
        value: "openai / gpt-4.1 / https://api.example.test/v1",
        raw: "provider: openai / gpt-4.1 / https://api.example.test/v1",
      },
      {
        icon: "📥",
        label: "queued input",
        value: "2/5",
        raw: "queued input: 2/5",
      },
    ],
  });
});

test("formatStatusOutput leaves non-status slash output alone", () => {
  assert.equal(formatStatusOutput("No eligible context to compact."), null);
});
