import type { ReactNode } from "react";
import { InspectorSection } from "@/components/thread/InspectorSection";

export function ModuleStatusControl({ label, title, active, readOnly, stale, children }: {
  label: string; title: string; active: boolean; readOnly: boolean; stale?: string; children: ReactNode;
}) {
  return <InspectorSection title={title} summary={label} active={active}>
    {readOnly ? <div className="text-muted-foreground">Read only</div> : null}
    {stale ? <div role="status" className="text-muted-foreground">Showing the last received state.</div> : null}
    {children}
  </InspectorSection>;
}

export function ModuleStatusRow({ label, value }: { label: string; value: string }) {
  return <div className="grid grid-cols-[5rem_minmax(0,1fr)] gap-2">
    <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
    <span className="min-w-0 [overflow-wrap:anywhere]">{value}</span>
  </div>;
}
