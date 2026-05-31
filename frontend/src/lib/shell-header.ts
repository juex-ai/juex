export type ShellMCPSummary = {
  configured: number;
  connected: number;
  errors: number;
};

export type ShellMCPTone = "none" | "ok" | "error";

export type ShellMCPBadge = {
  label: string;
  tone: ShellMCPTone;
  title: string;
};

export function formatShellUpdatedAt(
  value?: string | null,
  locale?: Intl.LocalesArgument,
  options?: Pick<Intl.DateTimeFormatOptions, "timeZone">,
) {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;

  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
    ...options,
  }).format(date);
}

export function shellMCPBadge(status: ShellMCPSummary): ShellMCPBadge {
  const configured = Math.max(0, status.configured);
  const connected = Math.max(0, status.connected);
  const errors = Math.max(0, status.errors);
  const label = `MCP ${configured}`;

  if (configured === 0) {
    return {
      label,
      tone: "none",
      title: "No MCP servers configured",
    };
  }

  const titleBase = `MCP ${connected}/${configured} connected`;
  if (errors > 0 || connected < configured) {
    return {
      label,
      tone: "error",
      title:
        errors > 0
          ? `${titleBase}, ${errors} ${errors === 1 ? "error" : "errors"}`
          : titleBase,
    };
  }

  return {
    label,
    tone: "ok",
    title: titleBase,
  };
}

export function shellUpdatedAtClassName() {
  return "hidden font-mono text-[11px] text-muted-foreground xl:inline";
}
