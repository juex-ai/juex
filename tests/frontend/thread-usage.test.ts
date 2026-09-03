import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  buildThreadUsageView,
  formatThreadTokenCount,
} from "../../frontend/src/lib/thread-usage.ts";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

test("formatThreadTokenCount keeps small counts exact and compacts large totals", () => {
  assert.equal(formatThreadTokenCount(0), "0");
  assert.equal(formatThreadTokenCount(999), "999");
  assert.equal(formatThreadTokenCount(1_250), "1.3k");
  assert.equal(formatThreadTokenCount(999_999), "1m");
  assert.equal(formatThreadTokenCount(1_250_000), "1.3m");
});

test("buildThreadUsageView excludes cached input from the total", () => {
  const view = buildThreadUsageView({
    total: {
      input_tokens: 1_000,
      cached_input_tokens: 750,
      output_tokens: 250,
    },
    by_model: {},
  });

  assert.equal(view.totalTokens, 1_250);
  assert.equal(view.summaryLabel, "1.3k tokens");
  assert.deepEqual(view.total, {
    inputTokens: 1_000,
    cachedInputTokens: 750,
    outputTokens: 250,
  });
  assert.deepEqual(view.models, []);
});

test("buildThreadUsageView returns one provider:model breakdown", () => {
  const view = buildThreadUsageView({
    total: { input_tokens: 10, output_tokens: 5 },
    by_model: {
      "openai:gpt-5": {
        input_tokens: 10,
        cached_input_tokens: 4,
        output_tokens: 5,
      },
    },
  });

  assert.deepEqual(view.models, [
    {
      modelRef: "openai:gpt-5",
      inputTokens: 10,
      cachedInputTokens: 4,
      outputTokens: 5,
      totalTokens: 15,
    },
  ]);
});

test("buildThreadUsageView sorts multiple models by input plus output with stable ties", () => {
  const view = buildThreadUsageView({
    total: { input_tokens: 81, output_tokens: 42 },
    by_model: {
      "zeta:tie": { input_tokens: 10, output_tokens: 10 },
      "openai:largest": { input_tokens: 50, output_tokens: 10 },
      "alpha:tie": { input_tokens: 5, output_tokens: 15 },
      "small:last": { input_tokens: 2, output_tokens: 1 },
    },
  });

  assert.deepEqual(
    view.models.map(({ modelRef, totalTokens }) => ({ modelRef, totalTokens })),
    [
      { modelRef: "openai:largest", totalTokens: 60 },
      { modelRef: "alpha:tie", totalTokens: 20 },
      { modelRef: "zeta:tie", totalTokens: 20 },
      { modelRef: "small:last", totalTokens: 3 },
    ],
  );
});

test("Thread usage disclosure is keyboard-focusable and replaces inline row details", () => {
  const componentSource = source(
    "../../frontend/src/components/thread/ThreadUsageSummary.tsx",
  );
  const explorerSource = source("../../frontend/src/pages/ThreadExplorer.tsx");

  assert.match(componentSource, /<TooltipProvider>/);
  assert.match(componentSource, /<TooltipTrigger asChild>/);
  assert.match(componentSource, /<button[\s\S]*type="button"/);
  assert.match(componentSource, /aria-label=\{`\$\{view\.summaryLabel\}\. Show token usage details`\}/);
  assert.match(componentSource, /focus-visible:ring-2/);
  assert.match(explorerSource, /<ThreadUsageSummary usage=\{thread\.token_usage\} \/>/);
  assert.doesNotMatch(explorerSource, /cached ·/);
  assert.doesNotMatch(explorerSource, /getThread\(/);
});
