const MESSAGE_RESPONSE_CLASS_NAME =
  "juex-markdown size-full [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 [&_code]:font-mono [&_p]:whitespace-pre-wrap [&_pre]:rounded-[10px]";

const MESSAGE_CONTENT_BASE_CLASS_NAME =
  "flex w-fit min-w-0 flex-col gap-2 overflow-hidden text-[14.5px] leading-[1.6]";

const MESSAGE_CONTENT_USER_CLASS_NAME =
  "group-[.is-user]:ml-auto group-[.is-user]:max-w-[92%] group-[.is-user]:rounded-[18px] group-[.is-user]:rounded-tr-[6px] group-[.is-user]:border group-[.is-user]:border-border group-[.is-user]:bg-card group-[.is-user]:px-4 group-[.is-user]:py-3 group-[.is-user]:text-card-foreground group-[.is-user]:shadow-[var(--shadow-xs)] sm:group-[.is-user]:max-w-[78%]";

const MESSAGE_CONTENT_ASSISTANT_CLASS_NAME =
  "group-[.is-assistant]:max-w-[96%] group-[.is-assistant]:rounded-[18px] group-[.is-assistant]:rounded-tl-[6px] group-[.is-assistant]:border group-[.is-assistant]:border-border group-[.is-assistant]:bg-card group-[.is-assistant]:px-4 group-[.is-assistant]:py-3 group-[.is-assistant]:text-card-foreground group-[.is-assistant]:shadow-[var(--shadow-xs)] sm:group-[.is-assistant]:max-w-[82%]";

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
