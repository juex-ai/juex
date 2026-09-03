import type { ThreadTokenUsage, TokenUsage } from "../types";

export interface ThreadUsageCounts {
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
}

export interface ThreadUsageModelRow extends ThreadUsageCounts {
  modelRef: string;
  totalTokens: number;
}

export interface ThreadUsageView {
  summaryLabel: string;
  totalTokens: number;
  total: ThreadUsageCounts;
  models: ThreadUsageModelRow[];
}

export function formatThreadTokenCount(value: number): string {
  const tokens = normalizedTokenCount(value);
  if (tokens < 1_000) return String(tokens);
  if (tokens < 1_000_000) {
    const thousands = Math.round(tokens / 100) / 10;
    if (thousands < 1_000) return `${thousands}k`;
  }
  return `${Math.round(tokens / 100_000) / 10}m`;
}

export function formatExactThreadTokenCount(value: number): string {
  return normalizedTokenCount(value).toLocaleString("en-US");
}

export function buildThreadUsageView(usage: ThreadTokenUsage): ThreadUsageView {
  const total = usageCounts(usage.total);
  const totalTokens = total.inputTokens + total.outputTokens;
  const models = Object.entries(usage.by_model)
    .map(([modelRef, modelUsage]) => {
      const counts = usageCounts(modelUsage);
      return {
        modelRef,
        ...counts,
        totalTokens: counts.inputTokens + counts.outputTokens,
      };
    })
    .sort((left, right) => {
      const byTotal = right.totalTokens - left.totalTokens;
      if (byTotal !== 0) return byTotal;
      if (left.modelRef === right.modelRef) return 0;
      return left.modelRef < right.modelRef ? -1 : 1;
    });

  return {
    summaryLabel: `${formatThreadTokenCount(totalTokens)} tokens`,
    totalTokens,
    total,
    models,
  };
}

function usageCounts(usage: TokenUsage): ThreadUsageCounts {
  return {
    inputTokens: normalizedTokenCount(usage.input_tokens),
    cachedInputTokens: normalizedTokenCount(usage.cached_input_tokens ?? 0),
    outputTokens: normalizedTokenCount(usage.output_tokens),
  };
}

function normalizedTokenCount(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 0;
  return Math.trunc(value);
}
