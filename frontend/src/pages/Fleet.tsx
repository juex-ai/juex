import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowRight,
  FileCog,
  Play,
  RefreshCw,
  RotateCw,
  ScrollText,
  Square,
} from "lucide-react";

import { listAgents, runAgentAction } from "@/api";
import { LogoMark } from "@/components/LogoMark";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { agentPagePath } from "@/lib/fleet-routes";
import { cn } from "@/lib/utils";
import type { AgentRuntimeHealth, AgentStatus } from "@/types";

type LifecycleAction = "start" | "stop" | "restart";

export function Fleet() {
  const [agents, setAgents] = useState<AgentStatus[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busyAgent, setBusyAgent] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async ({ quiet = false } = {}) => {
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      setAgents(await listAgents());
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Failed to load fleet roster.",
      );
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void refresh({ quiet: true });
    const timer = window.setInterval(() => void refresh({ quiet: true }), 10_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  async function runAction(agent: AgentStatus, action: LifecycleAction) {
    setBusyAgent(agent.id);
    setError(null);
    try {
      const next = await runAgentAction(agent.id, action);
      setAgents((current) =>
        current.map((item) => (item.id === next.id ? next : item)),
      );
    } catch (cause) {
      const actionError =
        cause instanceof Error
          ? cause.message
          : `Failed to ${action} ${agent.name || agent.id}.`;
      await refresh({ quiet: true });
      setError(actionError);
    } finally {
      setBusyAgent(null);
    }
  }

  return (
    <div className="flex h-svh min-h-0 flex-col overflow-hidden bg-background">
      <header className="flex h-[var(--juex-header-height)] shrink-0 items-center gap-3 border-b bg-card px-4 shadow-[var(--shadow-xs)]">
        <div className="flex items-center gap-2 text-primary">
          <LogoMark className="size-6" />
          <span className="font-serif text-2xl italic leading-tight">juex</span>
        </div>
        <span className="h-5 border-l" aria-hidden="true" />
        <span className="text-sm font-medium text-foreground">Fleet</span>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="ml-auto text-muted-foreground hover:text-foreground"
          onClick={() => void refresh()}
          disabled={refreshing}
          aria-label="Refresh fleet"
          title="Refresh fleet"
        >
          <RefreshCw className={cn("size-4", refreshing && "animate-spin")} />
        </Button>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-7xl flex-col gap-4 px-4 py-6 md:px-6">
          <div>
            <h1 className="text-xl font-semibold text-foreground">Agents</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              Registered workspaces and their current runtime state.
            </p>
          </div>

          {error ? (
            <div
              role="alert"
              className="rounded-md border border-destructive/35 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {error}
            </div>
          ) : null}

          <div className="overflow-x-auto rounded-md border bg-card shadow-[var(--shadow-xs)]">
            <div className="min-w-[58rem]">
              <div className="grid grid-cols-[minmax(13rem,1fr)_8rem_minmax(18rem,1.4fr)_9rem_13rem] bg-muted/60 text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
                <div className="px-3 py-2 font-medium">Agent</div>
                <div className="px-3 py-2 font-medium">Health</div>
                <div className="px-3 py-2 font-medium">Workspace</div>
                <div className="px-3 py-2 font-medium">Process</div>
                <div className="px-3 py-2 text-right font-medium">Actions</div>
              </div>
              {loading ? (
                <div className="px-4 py-8 text-sm text-muted-foreground">
                  Loading agents...
                </div>
              ) : agents.length === 0 ? (
                <div className="px-4 py-10 text-center text-sm text-muted-foreground">
                  No registered agents.
                </div>
              ) : (
                <div className="divide-y">
                  {agents.map((agent) => (
                    <AgentRow
                      key={agent.id}
                      agent={agent}
                      busy={busyAgent === agent.id}
                      onAction={(action) => void runAction(agent, action)}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      </main>
    </div>
  );
}

function AgentRow({
  agent,
  busy,
  onAction,
}: {
  agent: AgentStatus;
  busy: boolean;
  onAction: (action: LifecycleAction) => void;
}) {
  const base = agentPagePath(agent.id);
  return (
    <div className="grid grid-cols-[minmax(13rem,1fr)_8rem_minmax(18rem,1.4fr)_9rem_13rem] items-center text-sm">
      <div className="min-w-0 px-3 py-3">
        <Link
          to={base}
          className="block truncate font-medium text-foreground outline-none hover:text-primary focus-visible:ring-2 focus-visible:ring-ring/35"
        >
          {agent.name || agent.id}
        </Link>
        <div className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
          {agent.id}
        </div>
        {agent.problem ? (
          <div className="mt-1 line-clamp-2 text-xs text-destructive" title={agent.problem}>
            {agent.problem}
          </div>
        ) : null}
      </div>
      <div className="px-3 py-3">
        <HealthBadge health={agent.runtime_health} />
        <div className="mt-1 font-mono text-[10px] text-muted-foreground">
          {agent.binding}
        </div>
      </div>
      <div
        className="truncate px-3 py-3 font-mono text-xs text-muted-foreground"
        title={agent.workspace || "Workspace unavailable"}
      >
        {agent.workspace || "-"}
      </div>
      <div className="px-3 py-3 font-mono text-xs text-muted-foreground">
        {agent.pid ? `pid ${agent.pid}` : "-"}
      </div>
      <div className="flex items-center justify-end gap-1 px-3 py-3">
        <TooltipProvider delayDuration={250}>
          <AgentAction
            label="Start agent"
            disabled={busy || agent.runtime_health === "healthy"}
            onClick={() => onAction("start")}
            icon={<Play className="size-3.5" />}
          />
          <AgentAction
            label="Stop agent"
            disabled={busy || agent.runtime_health === "stopped"}
            onClick={() => onAction("stop")}
            icon={<Square className="size-3.5" />}
          />
          <AgentAction
            label="Restart agent"
            disabled={busy || agent.runtime_health === "stopped"}
            onClick={() => onAction("restart")}
            icon={<RotateCw className={cn("size-3.5", busy && "animate-spin")} />}
          />
          <AgentLink label="View logs" to={`${base}/logs`} icon={<ScrollText className="size-3.5" />} />
          <AgentLink label="Edit config" to={`${base}/config`} icon={<FileCog className="size-3.5" />} />
          <AgentLink label="Open agent" to={base} icon={<ArrowRight className="size-3.5" />} />
        </TooltipProvider>
      </div>
    </div>
  );
}

function AgentAction({
  label,
  disabled,
  onClick,
  icon,
}: {
  label: string;
  disabled: boolean;
  onClick: () => void;
  icon: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          disabled={disabled}
          onClick={onClick}
          aria-label={label}
        >
          {icon}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function AgentLink({
  label,
  to,
  icon,
}: {
  label: string;
  to: string;
  icon: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button asChild variant="ghost" size="icon-sm">
          <Link to={to} aria-label={label}>
            {icon}
          </Link>
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function HealthBadge({ health }: { health: AgentRuntimeHealth }) {
  return (
    <Badge
      variant="outline"
      className={cn(
        "font-mono text-[10px]",
        health === "healthy" && "border-emerald-600/35 text-emerald-700",
        health === "unhealthy" && "border-destructive/35 text-destructive",
        health === "ambiguous" && "border-amber-600/35 text-amber-700",
      )}
    >
      {health}
    </Badge>
  );
}
