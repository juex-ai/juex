import type { MediaRef } from "../types";

export function mediaReferenceText(label: string, media?: MediaRef | null): string {
  if (!media) return `[${label}: missing media reference]`;
  const parts: string[] = [];
  if (media.artifact_path) parts.push(`path=${media.artifact_path}`);
  if (media.media_type) parts.push(`type=${media.media_type}`);
  if (media.sha256) parts.push(`sha256=${media.sha256}`);
  if (media.original_bytes) parts.push(`bytes=${media.original_bytes}`);
  if (media.width && media.height) parts.push(`size=${media.width}x${media.height}`);
  return parts.length > 0
    ? `[${label}: ${parts.join(" ")}]`
    : `[${label}: empty media reference]`;
}
