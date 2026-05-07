import { cn } from "@/lib/utils";

export type Status =
  | { kind: "idle" }
  | { kind: "running" }
  | { kind: "tool"; name: string }
  | { kind: "done" }
  | { kind: "error"; detail?: string };

const styles: Record<Status["kind"], string> = {
  idle: "bg-muted text-muted-foreground",
  running: "bg-juex-pending/15 text-juex-pending",
  tool: "bg-juex-tool/15 text-juex-tool",
  done: "bg-juex-done/15 text-juex-done",
  error: "bg-juex-error/15 text-juex-error",
};

export function StatusPill({ status }: { status: Status }) {
  const label =
    status.kind === "idle"
      ? "idle"
      : status.kind === "running"
        ? "running..."
        : status.kind === "tool"
          ? `tool: ${status.name}`
          : status.kind === "done"
            ? "done"
            : "error";
  const isAnimated = status.kind === "running" || status.kind === "tool";
  return (
    <span
      className={cn(
        "inline-flex items-center gap-2 rounded-full px-3 py-1 text-xs font-medium",
        styles[status.kind],
      )}
    >
      <span
        className={cn(
          "size-1.5 rounded-full bg-current",
          isAnimated && "animate-pulse",
        )}
      />
      {label}
    </span>
  );
}
