export type SystemNoticeDisplay = {
  title: string;
  content: string;
};

export function formatSystemNotice(text: string): SystemNoticeDisplay {
  const content = text
    .trim()
    .replace(/^system notice:\s*/i, "")
    .trim();
  return {
    title: /\bagent\s+(?:was\s+)?restarted\b/i.test(content)
      ? "Agent restarted"
      : "System notice",
    content,
  };
}
