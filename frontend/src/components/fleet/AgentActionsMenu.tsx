import { useRef, useState } from "react";
import { CircleCheck, CircleOff, LoaderCircle, MoreHorizontal, Play, RotateCw, Square, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import type { AgentLifecycleAction, AgentManagementAction } from "@/lib/fleet-shell";
import type { AgentStatus } from "@/types";

export type AgentMenuAction = AgentManagementAction | "enable" | "disable";
type ConfirmedAction = "stop" | "restart" | "disable";
const labels = { stop: "Stop", restart: "Restart", disable: "Disable" };
const effects = {
  stop: "Stopping interrupts active work across this agent’s Threads. Pending inputs cannot run while the agent is stopped. Start the agent again to continue using it.",
  restart: "Restarting interrupts active work across this agent’s Threads and briefly disconnects the runtime. Juex attempts to resume interrupted work after restart; pending inputs wait for the runtime to return.",
  disable: "Disabling stops this agent and interrupts its active work. Pending inputs cannot run until it is enabled and started again. Automatic startup is blocked while disabled.",
};

export function AgentActionsMenu({ agent, busy, primaryAction, onAction, management }: {
  agent: AgentStatus;
  busy: boolean;
  primaryAction: AgentLifecycleAction;
  onAction: (action: AgentLifecycleAction) => Promise<string | void>;
  management?: { onAction: (action: AgentMenuAction) => Promise<string | void>; onRemove: () => void };
}) {
  const [confirmation, setConfirmation] = useState<ConfirmedAction | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [executing, setExecuting] = useState(false);
  const executingRef = useRef(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const name = agent.name || agent.id;
  const locked = busy || executing;

  async function execute(action: AgentMenuAction) {
    if (executingRef.current || busy) return;
    executingRef.current = true;
    setExecuting(true);
    setError(null);
    try {
      const message = action === "start" || action === "stop"
        ? await onAction(action) : await management?.onAction(action);
      if (message) setError(message);
      else setConfirmation(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Agent operation failed.");
    } finally {
      executingRef.current = false;
      setExecuting(false);
    }
  }
  function request(action: AgentMenuAction) {
    setError(null);
    if (action === "stop" || action === "restart" || action === "disable") setConfirmation(action);
    else void execute(action);
  }

  return <>
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button ref={triggerRef} type="button" variant="ghost" size="icon" className="size-9" disabled={locked} aria-label={`Actions for ${name}`} title={`Actions for ${name}`}>
          {locked ? <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" /> : <MoreHorizontal className="size-4" />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-48" onCloseAutoFocus={(event) => { if (confirmation) event.preventDefault(); }}>
        <DropdownMenuLabel className="max-w-64 truncate" title={name}>{name}</DropdownMenuLabel>
        <DropdownMenuItem className="min-h-10" disabled={locked || (primaryAction === "start" && !agent.enabled)} onSelect={() => request(primaryAction)}>
          {primaryAction === "start" ? <Play /> : <Square />}{primaryAction === "start" ? "Start agent" : "Stop agent"}
        </DropdownMenuItem>
        {management ? <>
          <DropdownMenuItem className="min-h-10" disabled={locked || !agent.enabled || agent.runtime_health === "stopped"} onSelect={() => request("restart")}><RotateCw />Restart agent</DropdownMenuItem>
          <DropdownMenuItem className="min-h-10" disabled={locked} onSelect={() => request(agent.enabled ? "disable" : "enable")}>
            {agent.enabled ? <CircleOff /> : <CircleCheck />}{agent.enabled ? "Disable agent" : "Enable agent"}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem className="min-h-10" variant="destructive" disabled={locked} onSelect={management.onRemove}><Trash2 />Remove agent</DropdownMenuItem>
        </> : null}
      </DropdownMenuContent>
    </DropdownMenu>
    <Dialog open={confirmation !== null || error !== null} onOpenChange={(open) => { if (!open && !executingRef.current) { setConfirmation(null); setError(null); } }}>
      <DialogContent showCloseButton={false} onOpenAutoFocus={(event) => { event.preventDefault(); cancelRef.current?.focus(); }} onCloseAutoFocus={(event) => { event.preventDefault(); triggerRef.current?.focus(); }}>
        <DialogHeader>
          <DialogTitle className="break-words">{confirmation ? `${labels[confirmation]} ${name}?` : `Unable to update ${name}`}</DialogTitle>
          <DialogDescription>{confirmation ? effects[confirmation] : "The agent operation failed. Close this message to return to the action menu."}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1 text-sm" aria-live="polite">
          <p className="break-all font-mono text-xs text-muted-foreground">{agent.id}</p>
          <p>{agent.runtime_health === "healthy" && agent.activity ? (agent.activity.state === "working" ? "Working" : "Idle") : "Current work status unavailable"}</p>
          <p>{agent.activity ? `${agent.activity.pending_input_count} pending inputs` : "Pending input count unavailable"}</p>
        </div>
        {error ? <p role="alert" className="break-words text-sm text-destructive">{error}</p> : null}
        <DialogFooter>
          <Button ref={cancelRef} variant="outline" disabled={locked} onClick={() => { setConfirmation(null); setError(null); }}>{confirmation ? "Cancel" : "Close"}</Button>
          {confirmation ? <Button variant="destructive" disabled={locked} className="whitespace-normal break-words" onClick={() => { if (confirmation) void execute(confirmation); }}>
            {executing ? "Applying…" : `${confirmation ? labels[confirmation] : ""} ${name}`}
          </Button> : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </>;
}
