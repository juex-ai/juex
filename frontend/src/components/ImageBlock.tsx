import { DownloadIcon, ImageOffIcon, XIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";

import { getMediaURL } from "@/api";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import type { MediaRef } from "@/types";

type ImageBlockProps = {
  alt?: string;
  className?: string;
  media?: MediaRef | null;
  variant?: "card" | "thumbnail";
};

export function ImageBlock({
  alt,
  className,
  media,
  variant = "card",
}: ImageBlockProps) {
  const path = media?.artifact_path?.trim();
  const [failed, setFailed] = useState(false);
  const [previewFailed, setPreviewFailed] = useState(false);
  const captionID = useId();
  const meta = useMemo(() => mediaMetadata(media), [media]);
  const isThumbnail = variant === "thumbnail";

  if (!path || failed) {
    return (
      <div
        className={cn(
          isThumbnail
            ? "flex size-20 shrink-0 items-center justify-center rounded-lg border border-border/60 bg-muted/35 text-muted-foreground"
            : "flex max-w-[min(100%,32rem)] items-center gap-2 rounded-md border border-border/60 bg-muted/35 px-3 py-2 text-sm text-muted-foreground",
          className,
        )}
        role="status"
      >
        <ImageOffIcon className="size-4 shrink-0" aria-hidden="true" />
        <span className={isThumbnail ? "sr-only" : undefined}>
          {failed ? "Image failed to load" : "Image unavailable"}
        </span>
      </div>
    );
  }

  const src = getMediaURL(path);
  const name = mediaName(path);
  const aspectRatio =
    media?.width && media.height ? `${media.width} / ${media.height}` : undefined;

  return (
    <Dialog
      onOpenChange={(open) => {
        if (open) setPreviewFailed(false);
      }}
    >
      <figure
        className={cn(
          isThumbnail
            ? "size-20 shrink-0 overflow-hidden rounded-lg border border-border/60 bg-card shadow-[var(--shadow-xs)]"
            : "w-fit max-w-[min(100%,32rem)] overflow-hidden rounded-lg border border-border/60 bg-card shadow-[var(--shadow-xs)]",
          className,
        )}
      >
        <DialogTrigger asChild>
          <button
            type="button"
            className={cn(
              "block bg-background outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
              isThumbnail ? "size-full" : "w-full text-left",
            )}
            aria-label={isThumbnail ? `Preview ${name}` : undefined}
            aria-describedby={isThumbnail ? undefined : captionID}
          >
            <img
              src={src}
              alt={alt?.trim() || name}
              loading="lazy"
              className={
                isThumbnail
                  ? "size-full object-cover"
                  : "max-h-[24rem] w-full object-contain"
              }
              style={!isThumbnail && aspectRatio ? { aspectRatio } : undefined}
              onError={() => setFailed(true)}
            />
          </button>
        </DialogTrigger>
        {!isThumbnail ? (
          <figcaption
            id={captionID}
            className="flex min-w-0 items-center justify-between gap-2 border-t border-border/60 px-2.5 py-1.5 font-mono text-[11px] text-muted-foreground"
          >
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
        ) : null}
      </figure>

      <DialogContent
        className="h-[calc(100svh-2rem)] max-w-[calc(100vw-2rem)] place-items-center bg-transparent p-0 ring-0 shadow-none"
        showCloseButton={false}
      >
        <DialogTitle className="sr-only">Preview {name}</DialogTitle>
        <DialogDescription className="sr-only">
          Full-size image preview. Press Escape to close.
        </DialogDescription>
        <DialogClose asChild>
          <Button
            type="button"
            className="absolute right-2 top-2 z-10 border border-border/70 bg-background shadow-[var(--shadow-xs)]"
            aria-label="Close image preview"
            size="icon"
            variant="ghost"
          >
            <XIcon className="size-4" aria-hidden="true" />
          </Button>
        </DialogClose>
        {previewFailed ? (
          <div
            className="flex items-center gap-2 rounded-md border border-destructive/30 bg-background px-4 py-3 text-sm text-destructive"
            role="alert"
          >
            <ImageOffIcon className="size-4 shrink-0" aria-hidden="true" />
            Failed to load full-size image
          </div>
        ) : (
          <img
            src={src}
            alt={alt?.trim() || name}
            className="max-h-full max-w-full object-contain"
            onError={() => setPreviewFailed(true)}
          />
        )}
      </DialogContent>
    </Dialog>
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
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
