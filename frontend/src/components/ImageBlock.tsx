import { DownloadIcon, ImageOffIcon, XIcon } from "lucide-react";
import { useMemo, useState } from "react";

import { getMediaURL } from "@/api";
import { Button } from "@/components/ui/button";
import type { MediaRef } from "@/types";

export function ImageBlock({ media }: { media?: MediaRef | null }) {
  const path = media?.artifact_path?.trim();
  const [open, setOpen] = useState(false);
  const meta = useMemo(() => mediaMetadata(media), [media]);

  if (!path) {
    return (
      <div className="flex max-w-[min(100%,32rem)] items-center gap-2 rounded border border-border/60 bg-muted/35 px-3 py-2 text-sm text-muted-foreground">
        <ImageOffIcon className="size-4 shrink-0" aria-hidden="true" />
        <span>Image unavailable</span>
      </div>
    );
  }

  const src = getMediaURL(path);
  const name = mediaName(path);

  return (
    <>
      <figure className="max-w-[min(100%,34rem)] overflow-hidden rounded border border-border/60 bg-muted/25 shadow-[var(--shadow-xs)]">
        <button
          type="button"
          className="block w-full bg-background text-left"
          onClick={() => setOpen(true)}
        >
          <img
            src={src}
            alt={name}
            loading="lazy"
            className="max-h-[24rem] w-full object-contain"
          />
        </button>
        <figcaption className="flex min-w-0 items-center justify-between gap-2 border-t border-border/60 px-2.5 py-1.5 font-mono text-[11px] text-muted-foreground">
          <span className="min-w-0 truncate" title={meta}>
            {meta}
          </span>
          <Button
            asChild
            className="size-7 shrink-0"
            size="icon"
            variant="ghost"
          >
            <a href={src} download={name} aria-label={`Download ${name}`}>
              <DownloadIcon className="size-3.5" aria-hidden="true" />
            </a>
          </Button>
        </figcaption>
      </figure>
      {open ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-background/90 p-4 backdrop-blur"
          role="dialog"
          aria-modal="true"
          aria-label={name}
          onClick={() => setOpen(false)}
        >
          <button
            type="button"
            className="absolute right-4 top-4 inline-flex size-9 items-center justify-center rounded border border-border/70 bg-background text-foreground shadow-[var(--shadow-xs)]"
            aria-label="Close image preview"
            onClick={() => setOpen(false)}
          >
            <XIcon className="size-4" aria-hidden="true" />
          </button>
          <img
            src={src}
            alt={name}
            className="max-h-[calc(100vh-5rem)] max-w-[calc(100vw-2rem)] object-contain"
            onClick={(event) => event.stopPropagation()}
          />
        </div>
      ) : null}
    </>
  );
}

function mediaMetadata(media?: MediaRef | null): string {
  const path = media?.artifact_path ?? "";
  const parts = [mediaName(path)];
  if (media?.width && media.height) {
    parts.push(`${media.width}x${media.height}`);
  }
  if (media?.original_bytes) {
    parts.push(formatBytes(media.original_bytes));
  }
  return parts.join(" · ");
}

function mediaName(path: string): string {
  const clean = path.replace(/\\/g, "/").split("/").filter(Boolean).at(-1);
  return clean || "image";
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KB`;
}
