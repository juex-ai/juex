import { useState, type ReactNode } from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

export function InspectorSection({ title, summary, label, active, children }: {
  title: string; summary: string; label?: string; active?: boolean; children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return <details onToggle={(event) => setOpen(event.currentTarget.open)} className="group min-w-0 border-b border-border/70">
    <summary role="button" aria-expanded={open} aria-label={label ?? `Open ${title.toLowerCase()}: ${summary}`}
      className="flex min-h-14 cursor-pointer list-none items-center gap-2 px-4 py-3 outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35 [&::-webkit-details-marker]:hidden">
      <span className="min-w-0 flex-1">
        <span className="block text-xs font-medium text-foreground">{title}</span>
        <span className={cn("mt-0.5 block truncate text-[11px] text-muted-foreground", active && "text-primary")}>{summary}</span>
      </span>
      <ChevronDown className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden="true" />
    </summary>
    <div className="min-w-0 space-y-2 px-4 pb-4 text-xs [overflow-wrap:anywhere]">{children}</div>
  </details>;
}
