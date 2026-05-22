import { cn } from "@/lib/utils";

export type Status =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "pending"; count: number }
  | { kind: "tool"; name: string }
  | { kind: "done" }
  | { kind: "error"; detail?: string };

const styles: Record<Status["kind"], string> = {
  idle: "bg-muted text-muted-foreground",
  running:
    "border-juex-gold-300 bg-juex-gold-100 text-juex-gold-900 dark:border-juex-gold-400/30 dark:bg-juex-gold-400/10 dark:text-juex-gold-400",
  pending:
    "border-juex-gold-300 bg-juex-gold-100 text-juex-gold-900 dark:border-juex-gold-400/30 dark:bg-juex-gold-400/10 dark:text-juex-gold-400",
  tool: "border-juex-tool/20 bg-juex-tool-bg text-juex-tool",
  done: "border-juex-forest-300 bg-juex-forest-100 text-juex-done dark:border-juex-forest-300/25 dark:bg-juex-forest-400/10",
  error: "border-juex-error/25 bg-juex-error-bg text-juex-error",
};

export function StatusPill({ status }: { status: Status }) {
  const label =
    status.kind === "idle"
      ? "idle"
      : status.kind === "running"
        ? "running..."
        : status.kind === "pending"
          ? `pending ${status.count}`
        : status.kind === "tool"
          ? `tool: ${status.name}`
          : status.kind === "done"
            ? "done"
            : "error";
  const isAnimated =
    status.kind === "running" || status.kind === "pending" || status.kind === "tool";
  return (
    <div className="flex min-w-0 items-center gap-2">
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-2 rounded-full border px-3 py-1 font-mono text-[11px] font-medium",
          styles[status.kind],
        )}
      >
        <span
          className={cn(
            "size-1.5 rounded-full bg-current",
            isAnimated && "juex-live-dot",
          )}
        />
        {label}
      </span>
      {status.kind === "error" && status.detail ? (
        <span className="text-juex-error min-w-0 truncate font-mono text-[11px]" title={status.detail}>
          {status.detail}
        </span>
      ) : null}
    </div>
  );
}
