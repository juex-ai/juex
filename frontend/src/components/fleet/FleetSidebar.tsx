import {
  LoaderCircle,
  PanelLeftClose,
  PanelLeftOpen,
  Play,
  Plus,
  Settings,
  SlidersHorizontal,
  Square,
} from "lucide-react";
import { Link } from "react-router-dom";

import { LogoMark } from "@/components/LogoMark";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  agentStatusText,
  agentTabPath,
  agentVisualState,
  nextAgentLifecycleAction,
} from "@/lib/fleet-shell";
import { cn } from "@/lib/utils";
import type { AgentStatus } from "@/types";

type FleetSidebarProps = {
  agents: AgentStatus[];
  selectedAgentID: string;
  busyAgentID: string | null;
  collapsed: boolean;
  mobile?: boolean;
  onCollapse: () => void;
  onExpand: () => void;
  onNavigate?: () => void;
  onToggleLifecycle: (agent: AgentStatus) => void;
};

export function FleetSidebar({
  agents,
  selectedAgentID,
  busyAgentID,
  collapsed,
  mobile = false,
  onCollapse,
  onExpand,
  onNavigate,
  onToggleLifecycle,
}: FleetSidebarProps) {
  const compact = collapsed && !mobile;
  const workingCount = agents.filter(
    (agent) => agentVisualState(agent) === "working",
  ).length;
  const onlineCount = agents.filter(
    (agent) => agent.runtime_health === "healthy",
  ).length;
  const version =
    agents.find((agent) => agent.binary_version)?.binary_version ?? "local";

  return (
    <aside
      className={cn(
        "flex h-full shrink-0 flex-col overflow-hidden border-r bg-card transition-[width] duration-200",
        compact ? "w-16" : "w-[268px]",
        mobile && "w-[min(84vw,268px)]",
      )}
      aria-label="Fleet agents"
      data-collapsed={compact ? "true" : "false"}
    >
      <div className="flex h-[var(--juex-header-height)] shrink-0 items-center border-b px-3">
        {compact ? (
          <TooltipProvider delayDuration={200}>
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  className="group relative grid size-10 place-items-center rounded-md text-primary outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/35"
                  onClick={onExpand}
                  aria-label="Expand fleet sidebar"
                >
                  <LogoMark className="size-6 transition-opacity group-hover:opacity-0 group-focus-visible:opacity-0" />
                  <PanelLeftOpen className="absolute size-5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="right">Expand fleet sidebar</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        ) : (
          <>
            <Link
              to="/"
              className="flex min-w-0 flex-1 items-center gap-2 text-primary"
              onClick={onNavigate}
            >
              <LogoMark className="size-6 shrink-0" />
              <span className="font-serif text-2xl italic leading-tight">juex</span>
            </Link>
            {!mobile ? (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                onClick={onCollapse}
                aria-label="Collapse fleet sidebar"
                title="Collapse fleet sidebar"
              >
                <PanelLeftClose className="size-4" />
              </Button>
            ) : null}
          </>
        )}
      </div>

      <div
        className={cn(
          "shrink-0 border-b",
          compact ? "px-2 py-2" : "px-3 py-3",
        )}
      >
        {compact ? (
          <TooltipProvider delayDuration={200}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  asChild
                  variant="ghost"
                  size="icon"
                  className="w-full text-primary"
                >
                  <Link
                    to="/settings?add=1"
                    onClick={onNavigate}
                    aria-label="Add agent"
                  >
                    <Plus className="size-4" />
                  </Link>
                </Button>
              </TooltipTrigger>
              <TooltipContent side="right">Add agent</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        ) : (
          <>
            <div className="mb-2 flex items-center justify-between gap-2">
              <div className="min-w-0">
                <div className="text-xs font-semibold uppercase text-muted-foreground">
                  Fleet
                </div>
                <div className="truncate text-[11px] text-muted-foreground">
                  {workingCount} working · {onlineCount} online
                </div>
              </div>
              <Button asChild size="sm" className="shrink-0">
                <Link to="/settings?add=1" onClick={onNavigate}>
                  <Plus className="size-3.5" />
                  Add
                </Link>
              </Button>
            </div>
          </>
        )}
      </div>

      <nav className="min-h-0 flex-1 overflow-y-auto py-2" aria-label="Agents">
        {agents.map((agent) => (
          <AgentRailRow
            key={agent.id}
            agent={agent}
            selected={agent.id === selectedAgentID}
            compact={compact}
            busy={busyAgentID === agent.id}
            onNavigate={onNavigate}
            onToggleLifecycle={() => onToggleLifecycle(agent)}
          />
        ))}
      </nav>

      <div className="shrink-0 border-t p-2">
        <TooltipProvider delayDuration={200}>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                asChild
                variant="ghost"
                className={cn(
                  "w-full text-muted-foreground",
                  compact ? "justify-center px-0" : "justify-start",
                )}
              >
                <Link to="/settings" onClick={onNavigate}>
                  <SlidersHorizontal className="size-4 shrink-0" />
                  {!compact ? <span>Fleet settings</span> : null}
                </Link>
              </Button>
            </TooltipTrigger>
            {compact ? (
              <TooltipContent side="right">Fleet settings</TooltipContent>
            ) : null}
          </Tooltip>
        </TooltipProvider>
        {!compact ? (
          <div className="truncate px-2 pt-1 font-mono text-[10px] text-muted-foreground">
            {window.location.host} · {version}
          </div>
        ) : null}
      </div>
    </aside>
  );
}

function AgentRailRow({
  agent,
  selected,
  compact,
  busy,
  onNavigate,
  onToggleLifecycle,
}: {
  agent: AgentStatus;
  selected: boolean;
  compact: boolean;
  busy: boolean;
  onNavigate?: () => void;
  onToggleLifecycle: () => void;
}) {
  const state = agentVisualState(agent);
  const pendingCount = agent.activity?.pending_count ?? 0;
  const lifecycleAction = nextAgentLifecycleAction(agent);
  const name = agent.name || agent.id;

  return (
    <div
      className={cn(
        "group relative mx-2 mb-1 flex min-h-12 items-center rounded-md transition-colors",
        selected ? "bg-primary/10" : "hover:bg-muted/70",
        selected &&
          "before:absolute before:inset-y-1 before:left-0 before:w-[3px] before:rounded-r before:bg-[var(--juex-gold-400)]",
        compact && "justify-center",
      )}
      data-agent-state={state}
    >
      <Link
        to={agentTabPath(agent.id, "chat")}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-2 outline-none focus-visible:ring-2 focus-visible:ring-ring/35",
          compact ? "justify-center p-2" : "py-2 pl-3 pr-[4.75rem]",
        )}
        onClick={onNavigate}
        aria-label={`Open ${name}`}
        title={compact ? `${name}: ${agentStatusText(agent)}` : undefined}
      >
        <AgentAvatar agent={agent} state={state} compact={compact} />
        {!compact ? (
          <span className="min-w-0 flex-1">
            <span className="block truncate text-sm font-medium text-foreground">
              {name}
            </span>
            <span
              className={cn(
                "block truncate text-[11px] text-muted-foreground",
                state === "failed" && "text-destructive",
              )}
            >
              {agentStatusText(agent)}
            </span>
          </span>
        ) : null}
        {pendingCount > 0 ? (
          <span
            className={cn(
              "shrink-0 rounded-full bg-[var(--juex-gold-400)] font-mono font-semibold text-primary transition-opacity",
              compact
                ? "absolute right-0 top-0 min-w-4 px-1 text-center text-[9px]"
                : "px-1.5 py-0.5 text-[10px] group-hover:opacity-0 group-focus-within:opacity-0",
            )}
            title={`${pendingCount} pending inputs`}
          >
            {pendingCount}
          </span>
        ) : null}
      </Link>

      {!compact ? (
        <div className="absolute right-1.5 flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
          <TooltipProvider delayDuration={200}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-8 bg-card/90"
                  disabled={busy || (!agent.enabled && lifecycleAction === "start")}
                  onClick={onToggleLifecycle}
                  aria-label={`${lifecycleAction === "stop" ? "Stop" : "Start"} ${name}`}
                >
                  {busy ? (
                    <LoaderCircle className="size-3.5 animate-spin" />
                  ) : lifecycleAction === "stop" ? (
                    <Square className="size-3.5" />
                  ) : (
                    <Play className="size-3.5" />
                  )}
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                {lifecycleAction === "stop" ? "Stop agent" : "Start agent"}
              </TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  asChild
                  variant="ghost"
                  size="icon"
                  className="size-8 bg-card/90"
                >
                  <Link
                    to={agentTabPath(agent.id, "runtime")}
                    onClick={onNavigate}
                    aria-label={`Open ${name} runtime`}
                  >
                    <Settings className="size-3.5" />
                  </Link>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Runtime</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </div>
      ) : null}
    </div>
  );
}

function AgentAvatar({
  agent,
  state,
  compact,
}: {
  agent: AgentStatus;
  state: ReturnType<typeof agentVisualState>;
  compact: boolean;
}) {
  const name = agent.name || agent.id;
  const initial = name.trim().charAt(0).toUpperCase() || "J";
  return (
    <span
      className={cn(
        "relative grid shrink-0 place-items-center rounded-md bg-primary/10 font-serif font-semibold text-primary",
        compact ? "size-9 text-sm" : "size-8 text-xs",
      )}
      aria-hidden="true"
    >
      {initial}
      <span
        className={cn(
          "absolute -bottom-0.5 -right-0.5 size-2.5 rounded-full border-2 border-card",
          state === "stopped" && "bg-muted-foreground/55",
          state === "idle" && "bg-emerald-500",
          state === "working" &&
            "animate-pulse bg-emerald-500 motion-reduce:animate-none",
          state === "failed" && "bg-destructive",
        )}
      />
    </span>
  );
}
