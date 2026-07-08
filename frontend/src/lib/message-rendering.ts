const MESSAGE_RESPONSE_CLASS_NAME =
  "juex-markdown size-full [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 [&_code]:font-mono [&_p]:whitespace-pre-wrap [&_pre]:rounded-[10px]";

const MESSAGE_CONTENT_BASE_CLASS_NAME =
  "flex w-fit min-w-0 flex-col gap-2 overflow-hidden rounded-[18px] border border-border bg-card px-4 py-3 text-[14.5px] leading-[1.6] text-card-foreground shadow-[var(--shadow-xs)]";

const MESSAGE_CONTENT_USER_CLASS_NAME =
  "group-[.is-user]:ml-auto group-[.is-user]:max-w-[92%] group-[.is-user]:rounded-tr-[6px] sm:group-[.is-user]:max-w-[78%]";

const MESSAGE_CONTENT_ASSISTANT_CLASS_NAME =
  "group-[.is-assistant]:max-w-[96%] group-[.is-assistant]:rounded-tl-[6px] sm:group-[.is-assistant]:max-w-[82%]";

const EXTERNAL_EVENT_ROW_CLASS_NAME =
  "flex min-w-0 cursor-pointer list-none items-center gap-2 px-1 py-1.5 text-xs text-juex-gold-900 outline-none transition hover:text-juex-ink-900 focus-visible:ring-2 focus-visible:ring-ring/40 dark:text-juex-gold-400 dark:hover:text-juex-gold-200 [&::-webkit-details-marker]:hidden";

const EXTERNAL_EVENT_BODY_CLASS_NAME =
  "group relative mt-1.5 max-h-[15rem] overflow-auto rounded-md border border-juex-gold-300/70 bg-juex-gold-50/45 px-3 py-2.5 pr-11 text-[13px] leading-6 text-juex-ink-900 dark:border-juex-gold-400/25 dark:bg-juex-gold-400/5 dark:text-juex-cream-50";

const EXTERNAL_EVENT_COPY_CLASS_NAME =
  "absolute right-2 top-2 size-7 border border-transparent text-current opacity-0 transition-opacity hover:border-juex-gold-300 hover:bg-juex-gold-100 hover:text-current hover:opacity-100 focus-visible:ring-ring group-hover:opacity-100 group-focus-within:opacity-100 dark:hover:border-juex-gold-400/30 dark:hover:bg-juex-gold-400/10";

const PROCESS_DISCLOSURE_CLASS_NAME = "group/process-row w-full";

const PROCESS_DISCLOSURE_SUMMARY_CLASS_NAME =
  "inline-flex max-w-full cursor-pointer list-none items-center gap-2 py-1 font-mono text-[11px] leading-5 text-muted-foreground outline-none transition hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40 [&::-webkit-details-marker]:hidden";

const PROCESS_DISCLOSURE_BODY_CLASS_NAME =
  "ml-5 flex flex-col gap-2 pb-2 pt-0.5";

const PROCESS_STATUS_DOT_BASE_CLASS_NAME =
  "size-[5px] shrink-0 rounded-full";

const THINKING_DISCLOSURE_SUMMARY_CLASS_NAME =
  "inline-flex max-w-full cursor-pointer list-none items-center gap-1.5 py-1 font-mono text-[11px] leading-5 text-muted-foreground/75 outline-none transition hover:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/40 [&::-webkit-details-marker]:hidden";

const THINKING_DISCLOSURE_BODY_CLASS_NAME =
  "ml-5 max-w-[min(100%,42rem)] pb-2 pt-0.5 text-[13px] leading-6 text-foreground/80";

export function messageResponseClassName(className?: string) {
  return className
    ? `${MESSAGE_RESPONSE_CLASS_NAME} ${className}`
    : MESSAGE_RESPONSE_CLASS_NAME;
}

export function messageContentBaseClassName() {
  return MESSAGE_CONTENT_BASE_CLASS_NAME;
}

export function messageContentRoleClassName(role: "user" | "assistant") {
  return role === "user"
    ? MESSAGE_CONTENT_USER_CLASS_NAME
    : MESSAGE_CONTENT_ASSISTANT_CLASS_NAME;
}

export function externalEventRowClassName() {
  return EXTERNAL_EVENT_ROW_CLASS_NAME;
}

export function externalEventBodyClassName() {
  return EXTERNAL_EVENT_BODY_CLASS_NAME;
}

export function externalEventCopyClassName() {
  return EXTERNAL_EVENT_COPY_CLASS_NAME;
}

export function processDisclosureClassName(nested = false) {
  return nested
    ? `${PROCESS_DISCLOSURE_CLASS_NAME} ml-2`
    : PROCESS_DISCLOSURE_CLASS_NAME;
}

export function processDisclosureSummaryClassName() {
  return PROCESS_DISCLOSURE_SUMMARY_CLASS_NAME;
}

export function processDisclosureBodyClassName() {
  return PROCESS_DISCLOSURE_BODY_CLASS_NAME;
}

export function processStatusDotClassName(status: "done" | "failed") {
  return `${PROCESS_STATUS_DOT_BASE_CLASS_NAME} ${
    status === "failed" ? "bg-juex-error" : "bg-juex-done"
  }`;
}

export function thinkingDisclosureSummaryClassName() {
  return THINKING_DISCLOSURE_SUMMARY_CLASS_NAME;
}

export function thinkingDisclosureBodyClassName() {
  return THINKING_DISCLOSURE_BODY_CLASS_NAME;
}
