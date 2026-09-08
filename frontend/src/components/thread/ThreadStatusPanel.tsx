import { ThreadStatusSlot } from "@/modules/ThreadStatusSlot";
import { CircleGaugeIcon } from "lucide-react";

import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeTokenUsageDetailLabel,
} from "@/lib/runtime-display";
import type {
  ActiveContextSnapshot,
  AgentRuntimeStatusSnapshot,
  ContextUsage,
  ThreadShowResponse,
  TokenUsage,
} from "@/types";

const STATUS_CONTROL_CLASS =
  "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-sm border border-border/70 bg-background px-2 font-mono text-[11px] text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35 focus-visible:ring-offset-2 focus-visible:ring-offset-background";

export function ThreadStatusPanel({
  activeContext,
  data,
  runtimeStatus,
}: {
  activeContext?: ActiveContextSnapshot | null;
  data: ThreadShowResponse;
  runtimeStatus?: AgentRuntimeStatusSnapshot;
}) {
  return (
    <>
      {runtimeStatus ? (
        <ContextUsageLabel
          usage={runtimeStatus.context_usage}
          activeContext={activeContext}
          tokenUsage={runtimeStatus.token_usage}
        />
      ) : (
        <span className="text-xs text-muted-foreground">Context usage unavailable</span>
      )}
      <ThreadStatusSlot threadID={data.id} />
    </>
  );
}

function ContextUsageLabel({
  usage,
  activeContext,
  tokenUsage,
}: {
  usage?: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
  tokenUsage: TokenUsage;
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={STATUS_CONTROL_CLASS}
          aria-label={`Open context usage: ${runtimeContextPercentLabel(usage)}`}
        >
          <CircleGaugeIcon className="size-3" aria-hidden="true" />
          context {runtimeContextPercentLabel(usage)}
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="block max-h-[24rem] max-w-[calc(100vw-2rem)] space-y-1.5 overflow-auto font-mono text-xs"
      >
        {usage ? (
          <ContextUsageTooltip
            usage={usage}
            activeContext={activeContext}
            tokenUsage={tokenUsage}
          />
        ) : (
          <>
            <div>No context usage yet</div>
            <TokenUsageTooltipLine usage={tokenUsage} />
            <ActiveContextDebugLine snapshot={activeContext} />
          </>
        )}
      </PopoverContent>
    </Popover>
  );
}

function ContextUsageTooltip({
  usage,
  activeContext,
  tokenUsage,
}: {
  usage: ContextUsage;
  activeContext?: ActiveContextSnapshot | null;
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
      <ActiveContextDebugLine snapshot={activeContext} />
    </>
  );
}

function TokenUsageTooltipLine({ usage }: { usage: TokenUsage }) {
  return <div>{runtimeTokenUsageDetailLabel(usage)}</div>;
}

function ActiveContextDebugLine({
  snapshot,
}: {
  snapshot?: ActiveContextSnapshot | null;
}) {
  if (!snapshot) return null;
  const count = snapshot.messages?.length ?? 0;
  const tokens = snapshot.estimated_tokens ?? 0;
  return (
    <div className="text-muted-foreground">
      active provider context {count} messages, ~{formatTokenCount(tokens)}{" "}
      estimated tokens
    </div>
  );
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
