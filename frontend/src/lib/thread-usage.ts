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

export function aggregateThreadUsage(usages: readonly ThreadTokenUsage[]): ThreadTokenUsage {
  const total: TokenUsage = { input_tokens: 0, cached_input_tokens: 0, output_tokens: 0 };
  const models = new Map<string, TokenUsage>();
  const add = (target: TokenUsage, usage: TokenUsage) => {
    const counts = usageCounts(usage);
    target.input_tokens += counts.inputTokens;
    target.cached_input_tokens = (target.cached_input_tokens ?? 0) + counts.cachedInputTokens;
    target.output_tokens += counts.outputTokens;
  };
  for (const usage of usages) {
    add(total, usage.total);
    for (const [modelRef, modelUsage] of Object.entries(usage.by_model)) {
      const combined = models.get(modelRef) ?? { input_tokens: 0, cached_input_tokens: 0, output_tokens: 0 };
      add(combined, modelUsage);
      models.set(modelRef, combined);
    }
  }
  return { total, by_model: Object.fromEntries(models) };
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
