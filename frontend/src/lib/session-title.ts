export const EMPTY_SESSION_TITLE = "New Session";

export function sessionPreviewTitle(preview?: string | null): string {
  return preview?.trim() || EMPTY_SESSION_TITLE;
}
