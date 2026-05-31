const MESSAGE_RESPONSE_CLASS_NAME =
  "juex-markdown size-full [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 [&_code]:font-mono [&_p]:whitespace-pre-wrap [&_pre]:rounded-[10px]";

export function messageResponseClassName(className?: string) {
  return className
    ? `${MESSAGE_RESPONSE_CLASS_NAME} ${className}`
    : MESSAGE_RESPONSE_CLASS_NAME;
}
