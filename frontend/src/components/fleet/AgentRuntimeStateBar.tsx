import { LoaderCircle, Play } from "lucide-react";

import { Button } from "@/components/ui/button";
import { agentVisualState } from "@/lib/fleet-shell";
import { useFleetAgent } from "./FleetAgentContext";

export function AgentRuntimeStateBar() {
  const { agent, lifecycleBusy, startAgent } = useFleetAgent();
  if (!agent || agent.runtime_health === "healthy") return null;

  const failed = agentVisualState(agent) === "failed";
  return (
    <div
      className="flex min-h-14 items-center gap-3 rounded-md border bg-card px-3 py-2 shadow-[var(--shadow-xs)]"
      role="status"
      data-testid="agent-runtime-state-bar"
    >
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-foreground">
          {failed ? "Agent needs attention" : "Agent is stopped"}
        </div>
        <div className="truncate text-xs text-muted-foreground">
          {failed
            ? agent.problem || "Start the agent to retry its runtime."
            : "Conversation history remains available while the runtime is offline."}
        </div>
      </div>
      <Button
        type="button"
        size="sm"
        onClick={() => void startAgent()}
        disabled={lifecycleBusy || !agent.enabled}
      >
        {lifecycleBusy ? (
          <LoaderCircle className="size-3.5 animate-spin" />
        ) : (
          <Play className="size-3.5" />
        )}
        Start agent
      </Button>
    </div>
  );
}
