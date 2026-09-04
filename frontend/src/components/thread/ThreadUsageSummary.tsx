import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { badgeVariants } from "@/components/ui/badge";
import {
  buildThreadUsageView,
  formatExactThreadTokenCount,
  type ThreadUsageCounts,
} from "@/lib/thread-usage";
import { cn } from "@/lib/utils";
import type { ThreadTokenUsage } from "@/types";

export function ThreadUsageSummary({
  usage,
  className,
}: {
  usage: ThreadTokenUsage;
  className?: string;
}) {
  const view = buildThreadUsageView(usage);

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(
            badgeVariants({ variant: "outline" }),
            "cursor-pointer font-mono text-[11px] font-normal text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35",
            className,
          )}
          aria-label={`${view.summaryLabel}. Show token usage details`}
        >
          {view.summaryLabel}
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="end"
        sideOffset={6}
        aria-label="Token usage details"
        tabIndex={0}
        className="block !w-[min(22rem,calc(100vw-2rem))] !max-w-[calc(100vw-2rem)] max-h-[calc(100svh-2rem)] space-y-3 overflow-y-auto p-3 text-xs focus-visible:ring-2 focus-visible:ring-ring/35"
      >
        <div>
          <p className="font-semibold">Token usage</p>
          <p className="mt-0.5 text-muted-foreground">
            {formatExactThreadTokenCount(view.totalTokens)} total tokens
          </p>
        </div>
        <UsageCounts counts={view.total} />
        <div className="border-t border-border/60 pt-2">
          <p className="mb-2 font-semibold">By model</p>
          {view.models.length === 0 ? (
            <p className="text-muted-foreground">No model usage recorded.</p>
          ) : (
            <div className="space-y-2.5">
              {view.models.map((model) => (
                <div key={model.modelRef}>
                  <p className="break-all font-mono text-[11px] font-medium">
                    {model.modelRef}
                  </p>
                  <UsageCounts counts={model} />
                </div>
              ))}
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function UsageCounts({ counts }: { counts: ThreadUsageCounts }) {
  return (
    <dl className="grid grid-cols-3 gap-x-3 font-mono text-[11px] tabular-nums">
      <div>
        <dt className="text-muted-foreground">Input</dt>
        <dd>{formatExactThreadTokenCount(counts.inputTokens)}</dd>
      </div>
      <div>
        <dt className="text-muted-foreground">Cached</dt>
        <dd>{formatExactThreadTokenCount(counts.cachedInputTokens)}</dd>
      </div>
      <div>
        <dt className="text-muted-foreground">Output</dt>
        <dd>{formatExactThreadTokenCount(counts.outputTokens)}</dd>
      </div>
    </dl>
  );
}
