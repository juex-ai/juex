import { getMediaURL } from "@/api";
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
              {item.input || queuedImageLabel(item)}
            </div>
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
      ))}
    </div>
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
