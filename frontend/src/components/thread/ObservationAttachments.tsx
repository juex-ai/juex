import { FileIcon, FileTextIcon, LoaderCircleIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { getArtifactContent } from "@/api";
import { ImageBlock } from "@/components/ImageBlock";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { ObservationAttachmentDisplay } from "@/lib/mcp-events";
import type { FileContentResponse, MediaRef } from "@/types";

type FilePreview = {
  attachment: ObservationAttachmentDisplay;
  content?: FileContentResponse;
  error?: string;
  loading: boolean;
};

export function ObservationAttachments({
  attachments,
  images,
}: {
  attachments: ObservationAttachmentDisplay[];
  images: Array<MediaRef | null>;
}) {
  const [preview, setPreview] = useState<FilePreview | null>(null);
  const previewAbortRef = useRef<AbortController | null>(null);
  const imageAttachments = pairImageAttachments(images, attachments);
  const fileAttachments = attachments.filter((item) => item.kind === "file");

  useEffect(() => () => previewAbortRef.current?.abort(), []);

  async function openFile(attachment: ObservationAttachmentDisplay) {
    previewAbortRef.current?.abort();
    const controller = new AbortController();
    previewAbortRef.current = controller;
    setPreview({ attachment, loading: true });
    try {
      const content = await getArtifactContent(
        attachment.artifactPath,
        controller.signal,
      );
      if (previewAbortRef.current === controller) {
        setPreview({ attachment, content, loading: false });
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      if (previewAbortRef.current === controller) {
        setPreview({
          attachment,
          error:
            error instanceof Error
              ? error.message
              : "Failed to preview attachment.",
          loading: false,
        });
      }
    } finally {
      if (previewAbortRef.current === controller) previewAbortRef.current = null;
    }
  }

  function closePreview() {
    previewAbortRef.current?.abort();
    previewAbortRef.current = null;
    setPreview(null);
  }

  if (imageAttachments.length === 0 && fileAttachments.length === 0) return null;

  return (
    <div
      className="flex flex-col gap-2 border-t border-border/50 pt-3"
      data-observation-attachments
    >
      <span className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
        Attachments
      </span>
      <div className="flex flex-wrap gap-2">
        {imageAttachments.map(({ media, name }, index) => (
          <div
            key={`${media.artifact_path}:${index}`}
            title={name}
          >
            <ImageBlock
              alt={name}
              displayName={name}
              media={media}
              root="artifact"
              variant="thumbnail"
            />
          </div>
        ))}
        {fileAttachments.map((attachment) => {
          const name = attachmentName(
            attachment.sourcePath || attachment.artifactPath,
          );
          const Icon = isTextAttachment(attachment.mediaType)
            ? FileTextIcon
            : FileIcon;
          return (
            <button
              key={attachment.artifactPath}
              type="button"
              className="flex h-20 w-40 items-center gap-2 rounded-lg border border-border/60 bg-card px-3 text-left shadow-[var(--shadow-xs)] outline-none transition-colors hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring motion-reduce:transition-none"
              aria-label={`Preview ${name}`}
              onClick={() => void openFile(attachment)}
              data-observation-file-attachment
            >
              <Icon
                className="size-5 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <span className="min-w-0">
                <span className="block truncate text-xs font-medium" title={name}>
                  {name}
                </span>
                <span className="block truncate font-mono text-[10px] text-muted-foreground">
                  {attachmentTypeLabel(attachment)}
                </span>
              </span>
            </button>
          );
        })}
      </div>

      <Sheet
        open={preview !== null}
        onOpenChange={(open) => !open && closePreview()}
      >
        <SheetContent
          className="flex !w-full !max-w-none flex-col gap-0 border-l bg-card p-0 sm:!max-w-xl"
          side="right"
        >
          <SheetHeader className="border-b p-4">
            <SheetTitle className="break-all pr-8 font-mono text-sm text-foreground">
              {attachmentName(
                preview?.attachment.sourcePath ||
                  preview?.attachment.artifactPath,
              )}
            </SheetTitle>
            <SheetDescription className="sr-only">
              Observation attachment preview
            </SheetDescription>
            {preview?.content?.truncated ? (
              <div className="text-xs text-muted-foreground">
                Preview truncated at 256 KB.
              </div>
            ) : null}
          </SheetHeader>
          <div className="flex min-h-0 flex-1 overflow-auto bg-muted/40 p-4">
            {preview?.loading ? (
              <div
                className="flex items-center gap-2 text-sm text-muted-foreground"
                role="status"
              >
                <LoaderCircleIcon
                  className="size-4 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
                Loading attachment...
              </div>
            ) : preview?.error ? (
              <div className="text-sm text-destructive" role="alert">
                {preview.error}
              </div>
            ) : preview?.content?.kind === "image" ? (
              <ImageBlock
                media={{
                  artifact_path: preview.attachment.artifactPath,
                  media_type: preview.content.media_type,
                  original_bytes: preview.content.size,
                }}
                root="artifact"
              />
            ) : (
              <pre className="whitespace-pre-wrap break-words font-mono text-xs">
                {preview?.content?.content ?? ""}
              </pre>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

function attachmentName(path?: string): string {
  const name = path?.replace(/\\/g, "/").split("/").filter(Boolean).at(-1);
  return name || "attachment";
}

function pairImageAttachments(
  images: Array<MediaRef | null>,
  attachments: ObservationAttachmentDisplay[],
): Array<{ media: MediaRef; name: string }> {
  const remaining = attachments.filter((item) => item.kind === "image");
  const paired: Array<{ media: MediaRef; name: string }> = [];
  for (const media of images) {
    const artifactPath = media?.artifact_path?.trim();
    if (!media || !artifactPath) continue;
    const metadataIndex = remaining.findIndex(
      (item) => item.artifactPath === artifactPath,
    );
    const metadata = metadataIndex >= 0
      ? remaining.splice(metadataIndex, 1)[0]
      : undefined;
    paired.push({
      media,
      name: attachmentName(metadata?.sourcePath || artifactPath),
    });
  }
  return paired;
}

function isTextAttachment(mediaType: string): boolean {
  return mediaType.startsWith("text/") || /(?:json|xml|yaml)/i.test(mediaType);
}

function attachmentTypeLabel(attachment: ObservationAttachmentDisplay): string {
  const name = attachmentName(attachment.sourcePath);
  const dot = name.lastIndexOf(".");
  const type = dot > 0 && dot < name.length - 1
    ? name.slice(dot + 1).toUpperCase()
    : attachment.mediaType;
  return attachment.bytes > 0
    ? `${type} · ${formatBytes(attachment.bytes)}`
    : type;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
