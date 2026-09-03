import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  buildThreadUsageView,
  formatExactThreadTokenCount,
  type ThreadUsageCounts,
} from "@/lib/thread-usage";
import type { ThreadTokenUsage } from "@/types";

export function ThreadUsageSummary({ usage }: { usage: ThreadTokenUsage }) {
  const view = buildThreadUsageView(usage);

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            className="shrink-0 rounded-sm px-1.5 py-1 font-mono text-[11px] text-muted-foreground underline decoration-dotted underline-offset-4 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35"
            aria-label={`${view.summaryLabel}. Show token usage details`}
          >
            {view.summaryLabel}
          </button>
        </TooltipTrigger>
        <TooltipContent
          side="top"
          align="end"
          sideOffset={6}
          className="block w-[min(22rem,calc(100vw-2rem))] max-w-none space-y-3 p-3"
        >
          <div>
            <p className="font-semibold">Token usage</p>
            <p className="mt-0.5 text-background/70">
              {formatExactThreadTokenCount(view.totalTokens)} total tokens
            </p>
          </div>
          <UsageCounts counts={view.total} />
          <div className="border-t border-background/20 pt-2">
            <p className="mb-2 font-semibold">By model</p>
            {view.models.length === 0 ? (
              <p className="text-background/70">No model usage recorded.</p>
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
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

function UsageCounts({ counts }: { counts: ThreadUsageCounts }) {
  return (
    <dl className="grid grid-cols-3 gap-x-3 font-mono text-[11px] tabular-nums">
      <div>
        <dt className="text-background/70">Input</dt>
        <dd>{formatExactThreadTokenCount(counts.inputTokens)}</dd>
      </div>
      <div>
        <dt className="text-background/70">Cached</dt>
        <dd>{formatExactThreadTokenCount(counts.cachedInputTokens)}</dd>
      </div>
      <div>
        <dt className="text-background/70">Output</dt>
        <dd>{formatExactThreadTokenCount(counts.outputTokens)}</dd>
      </div>
    </dl>
  );
}
