import type { ReactNode } from "react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export function ModuleStatusControl({ label, title, active, readOnly, stale, children }: {
  label: string; title: string; active: boolean; readOnly: boolean; stale?: string; children: ReactNode;
}) {
  return <Popover>
    <PopoverTrigger asChild><button type="button" aria-label={`Open ${title.toLowerCase()}: ${label}`}
      className={cn("inline-flex h-7 shrink-0 items-center rounded-sm border border-border/70 bg-background px-2 font-mono text-[11px] outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/35", active ? "border-primary/30 text-primary" : "text-muted-foreground")}>
      {label}
    </button></PopoverTrigger>
    <PopoverContent align="start" className="block !w-[min(34rem,calc(100vw-2rem))] max-h-[24rem] overflow-auto text-left text-xs">
      <div className="space-y-2">
        <div className="font-mono font-semibold text-muted-foreground">{title}{readOnly ? " · Read only" : ""}</div>
        {stale ? <div role="status" className="text-muted-foreground">Showing the last received state.</div> : null}
        {children}
      </div>
    </PopoverContent>
  </Popover>;
}

export function ModuleStatusRow({ label, value }: { label: string; value: string }) {
  return <div className="grid gap-2 sm:grid-cols-[6rem_minmax(0,1fr)]">
    <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
    <span className="min-w-0 break-words text-popover-foreground">{value}</span>
  </div>;
}
