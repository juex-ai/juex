import { CircleIcon, CircleCheckIcon, CircleMinusIcon } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { InputStatus } from "@/types";
import { inputStatusLabel } from "./state";

export function InputCheckMarker({ status, scopeID }: { status: InputStatus; scopeID: string }) {
  const label = inputStatusLabel(status, scopeID);
  const Icon = status.checked_at ? CircleCheckIcon : status.scope_id === scopeID ? CircleIcon : CircleMinusIcon;
  return <Popover>
    <PopoverTrigger asChild>
      <button type="button" aria-label={label} title={label} className="inline-flex size-6 items-center justify-center rounded-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <Icon className="size-3" aria-hidden="true" />
      </button>
    </PopoverTrigger>
    <PopoverContent align="end" className="w-80 max-w-[calc(100vw-2rem)] space-y-3 text-xs">
      <div className="font-medium">{label}</div>
      <p className="text-muted-foreground">{status.checked_at
        ? "The Agent marked this input as handled. This is its acknowledgement, not an independent verification of the result."
        : status.scope_id === scopeID ? "This input is still on the Agent’s checklist. Work may already be in progress."
        : "A new context ended this checklist scope. This input was not marked as handled."}</p>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2">
        <dt className="text-muted-foreground">Input</dt><dd className="break-all font-mono">{status.input_id}</dd>
        <dt className="text-muted-foreground">Message</dt><dd className="break-all font-mono">{status.message_id}</dd>
        {status.checked_at ? <><dt className="text-muted-foreground">Checked</dt><dd>{new Date(status.checked_at).toLocaleString()}</dd></> : null}
        {status.tool_use_id ? <><dt className="text-muted-foreground">Tool call</dt><dd className="break-all font-mono">{status.tool_use_id}</dd></> : null}
      </dl>
    </PopoverContent>
  </Popover>;
}
