import {
  Folder,
  FolderOpen,
  Menu,
  MessagesSquare,
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
  agentTabPath,
  type AgentStageTab,
} from "@/lib/fleet-shell";
import { cn } from "@/lib/utils";
import type { AgentStatus } from "@/types";

const TABS: Array<{ id: AgentStageTab; label: string }> = [
  { id: "chat", label: "Chat" },
  { id: "runtime", label: "Runtime" },
];

export function FleetStageHeader({
  agent,
  contextTitle,
  threadStatus,
  threadID,
  activeTab,
  filePanelTitle,
  settings,
  workspaceOpen,
  onOpenMobileSidebar,
  onToggleWorkspace,
}: {
  agent: AgentStatus | null;
  contextTitle: string | null;
  threadStatus?: "Idle" | "Working" | "Failed" | "Archived" | "Unknown";
  threadID: string;
  activeTab: AgentStageTab;
  filePanelTitle: string;
  settings: boolean;
  workspaceOpen: boolean;
  onOpenMobileSidebar: () => void;
  onToggleWorkspace: () => void;
}) {
  const agentTitle = settings ? "Fleet settings" : agent?.name || agent?.id || "Fleet";
  const pageTitle = contextTitle || (threadID ? `Loading #${threadID}…` : activeTab === "runtime" ? "Runtime" : "Threads");
  const filePanelActionLabel = workspaceOpen
    ? `Hide ${filePanelTitle.toLowerCase()}`
    : `Show ${filePanelTitle.toLowerCase()}`;

  return (
    <header className="flex h-[var(--juex-header-height)] shrink-0 items-center gap-1 border-b bg-card px-2 shadow-[var(--shadow-xs)] sm:gap-2 md:px-4">
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

      <div className="flex min-w-0 max-w-[min(40vw,24rem)] flex-1 flex-col justify-center gap-0.5 sm:flex-initial">
        <div title={agentTitle} className="truncate text-sm font-semibold leading-4 text-foreground">
          {agentTitle}
        </div>
        {!settings ? (
          <div className="flex min-w-0 items-center gap-1.5 text-[11px] leading-3.5 text-muted-foreground">
            <span title={pageTitle} className="truncate">{pageTitle}</span>
            {threadID && threadStatus ? (
                <span
                  aria-label="Current Thread status"
                  className={cn(
                    "shrink-0 rounded-full border px-1.5 py-px text-[9px] font-medium leading-3",
                    threadStatus === "Working" &&
                      "border-[var(--juex-gold-400)]/50 bg-[var(--juex-gold-400)]/10 text-primary",
                    threadStatus === "Failed" &&
                      "border-destructive/40 bg-destructive/5 text-destructive",
                  )}
                >
                  {threadStatus}
                </span>
            ) : null}
          </div>
        ) : null}
      </div>

      {!settings && agent ? (
        <nav
          className="flex shrink-0 self-stretch sm:ml-2 sm:flex-1"
          aria-label="Agent views"
        >
          {TABS.map((tab) => (
            <Link
              key={tab.id}
              to={agentTabPath(agent.id, tab.id)}
              className={cn(
                "relative flex shrink-0 items-center px-1.5 text-xs font-medium text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35 sm:px-3",
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
                  to={`${agentTabPath(agent.id, "chat")}/threads`}
                  aria-label="Thread Explorer"
                >
                  <MessagesSquare className="size-4" />
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Thread Explorer</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="shrink-0"
                onClick={onToggleWorkspace}
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
