import { getMediaURL } from "@/api";
import type { QueuedInput } from "@/lib/queued-inputs";

export function QueuedInputStack({ items }: { items: QueuedInput[] }) {
  if (items.length === 0) return null;
  return (
    <section className="mb-2 min-h-0" aria-label="Queued inputs">
      <div className="mb-1.5 flex items-center justify-between gap-2 px-1 font-mono text-[10px] uppercase text-muted-foreground">
        <span>Queued inputs</span>
        <span aria-label={`${items.length} queued`}>{items.length}</span>
      </div>
      <div
        className="max-h-56 space-y-1.5 overflow-y-auto overscroll-contain pr-1"
        aria-live="polite"
      >
        {items.map((item, index) => {
          const label = item.input || queuedImageLabel(item);
          const reviewable = label.length > 80 || label.includes("\n");
          return (
            <div
              key={item.id}
              className="flex min-w-0 items-start gap-2 rounded-md border border-border/70 bg-card/90 px-3 py-2 text-left shadow-[var(--shadow-xs)]"
            >
              <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-[10px] text-muted-foreground">
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                {reviewable ? (
                  <details className="group">
                    <summary className="cursor-pointer list-none rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/35">
                      <span className="line-clamp-2 whitespace-pre-wrap break-words text-sm leading-5 text-foreground">
                        {label}
                      </span>
                      <span className="font-mono text-[10px] text-muted-foreground group-open:hidden">
                        Review
                      </span>
                      <span className="hidden font-mono text-[10px] text-muted-foreground group-open:inline">
                        Collapse
                      </span>
                    </summary>
                    <div className="mt-1 max-h-32 overflow-y-auto whitespace-pre-wrap break-words rounded-sm bg-muted/60 px-2 py-1.5 text-sm leading-5 text-foreground">
                      {label}
                    </div>
                  </details>
                ) : (
                  <div className="whitespace-pre-wrap break-words text-sm leading-5 text-foreground">
                    {label}
                  </div>
                )}
                {item.attachments?.length ? (
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    {item.attachments.map((media, mediaIndex) => {
                      const path = media.artifact_path;
                      if (!path) return null;
                      return (
                        <img
                          key={`${path}-${mediaIndex}`}
                          src={getMediaURL(path)}
                          alt={mediaName(path)}
                          className="size-10 rounded border border-border/70 object-cover"
                        />
                      );
                    })}
                  </div>
                ) : null}
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function queuedImageLabel(item: QueuedInput): string {
  const count = item.attachments?.length ?? 0;
  if (count === 1) return "1 image";
  if (count > 1) return `${count} images`;
  return "";
}

function mediaName(path: string): string {
  return path.replace(/\\/g, "/").split("/").filter(Boolean).at(-1) ?? "image";
}
