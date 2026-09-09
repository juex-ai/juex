import { ThreadStatusSlot } from "@/modules/ThreadStatusSlot";
import { InspectorSection } from "./InspectorSection";

import {
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeTokenUsageDetailLabel,
} from "@/lib/runtime-display";
import type {
  AgentRuntimeStatusSnapshot,
  ContextUsage,
  TokenUsage,
} from "@/types";

export function ThreadStatusPanel({
  threadID,
  runtimeStatus,
}: {
  threadID: string;
  runtimeStatus?: AgentRuntimeStatusSnapshot;
}) {
  return (
    <>
      {runtimeStatus ? (
        <ContextUsageLabel
          usage={runtimeStatus.context_usage}
          tokenUsage={runtimeStatus.token_usage}
        />
      ) : (
        <InspectorSection title="Context" summary="Unavailable"><p className="text-muted-foreground">Context usage unavailable</p></InspectorSection>
      )}
      <ThreadStatusSlot threadID={threadID} />
    </>
  );
}

function ContextUsageLabel({
  usage,
  tokenUsage,
}: {
  usage?: ContextUsage;
  tokenUsage: TokenUsage;
}) {
  return (
    <InspectorSection title="Context" summary={runtimeContextPercentLabel(usage)}>
      {usage ? <ContextUsageTooltip usage={usage} tokenUsage={tokenUsage} /> : <>
        <div>No context usage yet</div>
        <TokenUsageTooltipLine usage={tokenUsage} />
      </>}
    </InspectorSection>
  );
}

function ContextUsageTooltip({
  usage,
  tokenUsage,
}: {
  usage: ContextUsage;
  tokenUsage: TokenUsage;
}) {
  const windowTokens = usage.context_window ?? 0;
  return (
    <>
      <div>{runtimeContextModelLabel(usage)}</div>
      <div>{runtimeContextWindowDetailLabel(usage)}</div>
      <TokenUsageTooltipLine usage={tokenUsage} />
      {usage.cached_input_tokens ? (
        <div>
          cached input: {formatTokenCount(usage.cached_input_tokens)} tokens (
          {formatPercent(
            (usage.cached_input_tokens /
              Math.max(usage.input_tokens, 1)) *
              100,
          )}
          )
        </div>
      ) : null}
      <div className="text-muted-foreground">estimated breakdown</div>
      <div className="space-y-0.5">
        {(usage.breakdown ?? []).map((part) => (
          <div key={part.key}>
            - {part.label}: {formatTokenCount(part.tokens)} tokens
            {windowTokens > 0
              ? ` (${formatPercent((part.tokens / windowTokens) * 100)})`
              : ""}
          </div>
        ))}
      </div>
    </>
  );
}

function TokenUsageTooltipLine({ usage }: { usage: TokenUsage }) {
  return <div>{runtimeTokenUsageDetailLabel(usage)}</div>;
}

function formatTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return value === 0 ? "0" : "-";
  if (value >= 1_000_000) return `${trimFixed(value / 1_000_000)}m`;
  if (value >= 1_000) return `${trimFixed(value / 1_000)}k`;
  return Math.round(value).toString();
}

function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return "0%";
  if (value > 0 && value < 0.1) return "0.0%";
  return `${trimFixed(value)}%`;
}

function trimFixed(value: number): string {
  return value.toFixed(1).replace(/\.0$/, "");
}
