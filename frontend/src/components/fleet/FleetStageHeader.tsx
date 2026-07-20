import {
  Folder,
  FolderOpen,
  History,
  Menu,
} from "lucide-react";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  agentStateLabel,
  agentStatusText,
  agentTabPath,
  agentVisualState,
  type AgentStageTab,
} from "@/lib/fleet-shell";
import { cn } from "@/lib/utils";
import type { AgentStatus } from "@/types";

const TABS: Array<{ id: AgentStageTab; label: string }> = [
  { id: "chat", label: "Chat" },
  { id: "runtime", label: "Runtime" },
  { id: "observables", label: "Observables" },
  { id: "logs", label: "Logs" },
  { id: "config", label: "Config" },
];

export function FleetStageHeader({
  agent,
  activeTab,
  filePanelTitle,
  settings,
  workspaceOpen,
  onOpenMobileSidebar,
  onToggleWorkspace,
}: {
  agent: AgentStatus | null;
  activeTab: AgentStageTab;
  filePanelTitle: string;
  settings: boolean;
  workspaceOpen: boolean;
  onOpenMobileSidebar: () => void;
  onToggleWorkspace: () => void;
}) {
  const state = agent ? agentVisualState(agent) : "stopped";
  const filePanelActionLabel = workspaceOpen
    ? `Hide ${filePanelTitle.toLowerCase()}`
    : `Show ${filePanelTitle.toLowerCase()}`;

  return (
    <header className="flex min-h-[var(--juex-header-height)] shrink-0 items-center gap-2 border-b bg-card px-3 shadow-[var(--shadow-xs)] md:px-4">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="shrink-0 min-[760px]:hidden"
        onClick={onOpenMobileSidebar}
        aria-label="Open fleet agents"
      >
        <Menu className="size-4" />
      </Button>

      <div className="flex min-w-0 shrink-0 items-center gap-2">
        <div className="truncate text-sm font-semibold text-foreground">
          {settings ? "Fleet settings" : agent?.name || agent?.id || "Fleet"}
        </div>
        {!settings && agent ? (
          <span
            className={cn(
              "shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-medium leading-none text-muted-foreground",
              state === "working" &&
                "border-[var(--juex-gold-400)]/50 bg-[var(--juex-gold-400)]/10 text-primary",
              state === "failed" &&
                "border-destructive/40 bg-destructive/5 text-destructive",
            )}
            title={agentStatusText(agent)}
          >
            {agentStateLabel(agent)}
          </span>
        ) : null}
      </div>

      {!settings && agent ? (
        <nav
          className="ml-2 flex min-w-0 flex-1 self-stretch overflow-x-auto"
          aria-label="Agent views"
        >
          {TABS.map((tab) => (
            <Link
              key={tab.id}
              to={agentTabPath(agent.id, tab.id)}
              className={cn(
                "relative flex shrink-0 items-center px-2.5 text-xs font-medium text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35 sm:px-3",
                activeTab === tab.id &&
                  "text-primary after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:bg-[var(--juex-gold-400)]",
              )}
            >
              {tab.label}
            </Link>
          ))}
        </nav>
      ) : (
        <div className="flex-1" />
      )}

      {!settings && agent && activeTab === "chat" ? (
        <TooltipProvider delayDuration={200}>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                asChild
                variant="ghost"
                size="icon"
                className="shrink-0"
              >
                <Link
                  to={`${agentTabPath(agent.id, "chat")}/history`}
                  aria-label="Session history"
                >
                  <History className="size-4" />
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Session history</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="shrink-0"
                onClick={onToggleWorkspace}
                disabled={agent.runtime_health !== "healthy"}
                aria-label={filePanelActionLabel}
              >
                {workspaceOpen ? (
                  <FolderOpen className="size-4" />
                ) : (
                  <Folder className="size-4" />
                )}
              </Button>
            </TooltipTrigger>
            <TooltipContent>{filePanelActionLabel}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : null}
    </header>
  );
}
