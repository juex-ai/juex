const MESSAGE_RESPONSE_CLASS_NAME =
  "juex-markdown size-full [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 [&_code]:font-mono [&_p]:whitespace-pre-wrap [&_pre]:rounded-md";

const MESSAGE_CONTENT_BASE_CLASS_NAME =
  "flex w-fit min-w-0 flex-col gap-2 overflow-hidden rounded-lg border border-border bg-card px-4 py-3 text-[14.5px] leading-[1.6] text-card-foreground shadow-[var(--shadow-xs)]";

const MESSAGE_CONTENT_USER_CLASS_NAME =
  "group-[.is-user]:ml-auto group-[.is-user]:max-w-[92%] group-[.is-user]:rounded-[16px] group-[.is-user]:rounded-tr-md sm:group-[.is-user]:max-w-[78%]";

export function messageResponseClassName(className?: string) {
  return className
    ? `${MESSAGE_RESPONSE_CLASS_NAME} ${className}`
    : MESSAGE_RESPONSE_CLASS_NAME;
}

export function messageContentBaseClassName() {
  return MESSAGE_CONTENT_BASE_CLASS_NAME;
}

export function messageContentUserClassName() {
  return MESSAGE_CONTENT_USER_CLASS_NAME;
}

export type TranscriptDisclosureTone = "external" | "system";
