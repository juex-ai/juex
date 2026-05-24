import type { QueuedInput } from "@/lib/queued-inputs";

export function QueuedInputStack({ items }: { items: QueuedInput[] }) {
  if (items.length === 0) return null;
  return (
    <div className="mb-2 flex flex-col gap-1.5" aria-live="polite">
      {items.map((item, index) => (
        <div
          key={item.id}
          className="flex min-w-0 items-start gap-2 rounded-lg border border-border/70 bg-card/90 px-3 py-2 text-left shadow-[var(--shadow-xs)]"
        >
          <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-[10px] text-muted-foreground">
            {index + 1}
          </span>
          <div className="min-w-0 flex-1">
            <div className="font-mono text-[10px] uppercase text-muted-foreground">
              Queued
            </div>
            <div className="truncate text-sm leading-5 text-foreground">
              {item.input}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}
