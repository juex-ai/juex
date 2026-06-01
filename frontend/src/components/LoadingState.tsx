import { LogoMark } from "@/components/LogoMark";
import { loadingStateLabel } from "@/lib/loading-state";
import { cn } from "@/lib/utils";

type LoadingStateProps = {
  label?: string;
  className?: string;
};

export function LoadingState({ label, className }: LoadingStateProps) {
  const text = loadingStateLabel(label);

  return (
    <div
      className={cn(
        "flex min-h-0 flex-1 items-center justify-center bg-background px-4 py-8 text-center",
        className,
      )}
      role="status"
      aria-live="polite"
    >
      <div className="flex flex-col items-center gap-3">
        <div className="relative flex size-16 items-center justify-center text-primary">
          <span
            aria-hidden="true"
            className="absolute inset-0 rounded-full border border-primary/15 bg-card/80 shadow-[var(--shadow-sm)]"
          />
          <span
            aria-hidden="true"
            className="absolute inset-1 rounded-full border-2 border-primary/10 border-t-primary motion-safe:animate-spin"
          />
          <span
            aria-hidden="true"
            className="absolute inset-3 rounded-full bg-primary/5"
          />
          <LogoMark className="relative size-7 motion-safe:animate-pulse" />
        </div>
        <p className="font-mono text-xs font-medium uppercase tracking-[0.16em] text-muted-foreground">
          {text}
        </p>
      </div>
    </div>
  );
}
