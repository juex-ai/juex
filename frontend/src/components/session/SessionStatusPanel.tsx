import type { ReactNode } from "react";
import { CircleGaugeIcon, LoaderCircleIcon } from "lucide-react";

import { MessageResponse } from "@/components/ai-elements/message";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  formatRuntimeTimestamp,
  notesCheckboxProgress,
  runtimeContextModelLabel,
  runtimeContextPercentLabel,
  runtimeContextWindowDetailLabel,
  runtimeGoalContinuationLabel,
  runtimeSessionStateBadgeLabel,
  runtimeSessionStateIsActive,
  runtimeTokenUsageDetailLabel,
} from "@/lib/runtime-display";
import { cn } from "@/lib/utils";
import type {
  ActiveContextSnapshot,
  AgentRuntimeStatusSnapshot,
  ContextUsage,
  GoalStatusSnapshot,
  NotesSnapshot,
  SessionShowResponse,
  TokenUsage,
} from "@/types";

const STATUS_CONTROL_CLASS =
  "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-sm border border-border/70 bg-background px-2 font-mono text-[11px] text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35 focus-visible:ring-offset-2 focus-visible:ring-offset-background";

export function SessionStatusPanel({
  activeContext,
  data,
  runtimeStatus,
}: {
  activeContext?: ActiveContextSnapshot | null;
  data: SessionShowResponse;
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
        <StatusLoading />
      )}
      <SessionRuntimeStateBadge data={data} />
    </>
  );
}

function SessionRuntimeStateBadge({ data }: { data: SessionShowResponse }) {
  const label = runtimeSessionStateBadgeLabel(data.goal, data.notes);
  const active = runtimeSessionStateIsActive(data.goal, data.notes);
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          className={cn(
            STATUS_CONTROL_CLASS,
            active ? "border-primary/30 text-primary" : "text-muted-foreground",
          )}
          type="button"
          aria-label={`Open goal and notes: ${label}`}
        >
          {label}
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="block !w-[min(34rem,calc(100vw-2rem))] !max-w-[calc(100vw-2rem)] max-h-[24rem] overflow-auto text-left text-xs"
      >
        <SessionStateTooltip goal={data.goal} notes={data.notes} />
      </PopoverContent>
    </Popover>
  );
}

function SessionStateTooltip({
  goal,
  notes,
}: {
  goal?: GoalStatusSnapshot;
  notes?: NotesSnapshot;
}) {
  return (
    <div className="space-y-3">
      <GoalStateTooltip goal={goal} />
      <div className="border-t border-border/60 pt-3">
        <NotesStateTooltip notes={notes} />
      </div>
    </div>
  );
}

function GoalStateTooltip({ goal }: { goal?: GoalStatusSnapshot }) {
  if (!goal) {
    return (
      <RuntimeTooltipPanel title="Goal">
        <div className="text-muted-foreground">No goal state for this session.</div>
      </RuntimeTooltipPanel>
    );
  }
  return (
    <RuntimeTooltipPanel title="Goal">
      <RuntimeTooltipRow label="status" value={goal.status || "unknown"} />
      <RuntimeTooltipRow label="description" value={goal.description || "-"} />
      <RuntimeTooltipRow label="acceptance" value={goal.acceptance || "-"} />
      <RuntimeTooltipRow label="reason" value={goal.status_reason || "-"} />
      <RuntimeTooltipRow
        label="continuations"
        value={runtimeGoalContinuationLabel(goal)}
      />
      <RuntimeTooltipRow
        label="updated"
        value={formatRuntimeTimestamp(goal.updated_at)}
      />
    </RuntimeTooltipPanel>
  );
}

function NotesStateTooltip({ notes }: { notes?: NotesSnapshot }) {
  if (!notes?.content?.trim()) {
    return (
      <RuntimeTooltipPanel title="Notes">
        <div className="text-muted-foreground">
          No working notes for this session.
        </div>
      </RuntimeTooltipPanel>
    );
  }
  const progress = notesCheckboxProgress(notes);
  return (
    <RuntimeTooltipPanel title="Notes">
      <RuntimeTooltipRow
        label="updated"
        value={formatRuntimeTimestamp(notes.updated_at)}
      />
      {progress.total > 0 ? (
        <div className="space-y-1.5">
          <RuntimeTooltipRow
            label="progress"
            value={`${progress.completed}/${progress.total} complete`}
          />
          <div
            aria-label="Notes task progress"
            aria-valuemax={progress.total}
            aria-valuemin={0}
            aria-valuenow={progress.completed}
            className="h-1.5 w-full overflow-hidden rounded-sm bg-muted"
            role="progressbar"
          >
            <div
              className="h-full bg-primary transition-[width]"
              style={{ width: `${progress.percent}%` }}
            />
          </div>
        </div>
      ) : null}
      <div className="border-t border-border/60 pt-2">
        <MessageResponse className="break-words text-xs leading-relaxed [&_h1]:!my-2 [&_h1]:!text-base [&_h2]:!my-2 [&_h2]:!text-sm [&_h3]:!my-1.5 [&_h3]:!text-xs">
          {notes.content}
        </MessageResponse>
      </div>
    </RuntimeTooltipPanel>
  );
}

function RuntimeTooltipPanel({
  children,
  title,
}: {
  children: ReactNode;
  title: string;
}) {
  return (
    <div className="min-w-[18rem] max-w-xl space-y-2">
      <div className="font-mono text-[11px] font-semibold uppercase tracking-normal text-muted-foreground">
        {title}
      </div>
      <div className="space-y-1.5">{children}</div>
    </div>
  );
}

function RuntimeTooltipRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-2 sm:grid-cols-[6rem_minmax(0,1fr)]">
      <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words text-popover-foreground">{value}</span>
    </div>
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

function StatusLoading() {
  return (
    <div
      aria-label="Loading session status"
      className={STATUS_CONTROL_CLASS}
      role="status"
    >
      <LoaderCircleIcon
        className="size-3 animate-spin motion-reduce:animate-none"
        aria-hidden="true"
      />
      status
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
