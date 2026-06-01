export const EMPTY_SESSION_TITLE = "New Session";

export function sessionPreviewTitle(preview: string): string {
  return preview.trim() || EMPTY_SESSION_TITLE;
}
