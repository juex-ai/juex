const MESSAGE_RESPONSE_CLASS_NAME =
  "juex-markdown size-full [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 [&_code]:font-mono [&_p]:whitespace-pre-wrap [&_pre]:rounded-[10px]";

const MESSAGE_CONTENT_BASE_CLASS_NAME =
  "flex w-fit min-w-0 flex-col gap-2 overflow-hidden rounded-[18px] border border-border bg-card px-4 py-3 text-[14.5px] leading-[1.6] text-card-foreground shadow-[var(--shadow-xs)]";

const MESSAGE_CONTENT_USER_CLASS_NAME =
  "group-[.is-user]:ml-auto group-[.is-user]:max-w-[92%] group-[.is-user]:rounded-tr-[6px] sm:group-[.is-user]:max-w-[78%]";

const MESSAGE_CONTENT_ASSISTANT_CLASS_NAME =
  "group-[.is-assistant]:max-w-[96%] group-[.is-assistant]:rounded-tl-[6px] sm:group-[.is-assistant]:max-w-[82%]";

const EXTERNAL_EVENT_ROW_CLASS_NAME =
  "flex min-w-0 items-center gap-2 px-1 py-1.5 text-xs text-juex-gold-900 dark:text-juex-gold-400";

const EXTERNAL_EVENT_BODY_CLASS_NAME =
  "group relative border-t border-juex-gold-300/70 px-3 py-3 pr-11 text-[13px] leading-6 text-juex-ink-900 dark:border-juex-gold-400/20 dark:text-juex-cream-50";

const EXTERNAL_EVENT_COPY_CLASS_NAME =
  "absolute right-2 top-2 size-7 border border-transparent text-current opacity-0 transition-opacity hover:border-juex-gold-300 hover:bg-juex-gold-100 hover:text-current hover:opacity-100 focus-visible:ring-ring group-hover:opacity-100 group-focus-within:opacity-100 dark:hover:border-juex-gold-400/30 dark:hover:bg-juex-gold-400/10";

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
