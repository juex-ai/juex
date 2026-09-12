import {
  SlidersHorizontal,
  Menu,
  MessagesSquare,
} from "lucide-react";
import type { Ref } from "react";
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

export function FleetStageHeader({
  agent,
  contextTitle,
  threadStatus,
  threadID,
  activeTab,
  settings,
  onOpenMobileSidebar,
  mobileSidebarButtonRef,
}: {
  agent: AgentStatus | null;
  contextTitle: string | null;
  threadStatus?: "Idle" | "Working" | "Failed" | "Archived" | "Unknown";
  threadID: string;
  activeTab: AgentStageTab;
  settings: boolean;
  onOpenMobileSidebar: () => void;
  mobileSidebarButtonRef?: Ref<HTMLButtonElement>;
}) {
  const agentTitle = settings ? "Fleet settings" : agent?.name || agent?.id || "Fleet";
  const pageTitle = contextTitle || (threadID ? `Loading #${threadID}…` : activeTab === "runtime" ? "Runtime" : "Threads");

  return (
    <header className="flex h-[var(--juex-header-height)] shrink-0 items-center gap-1 border-b bg-card px-2 shadow-[var(--shadow-xs)] sm:gap-2 md:px-4">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-11 shrink-0 min-[760px]:hidden"
        onClick={onOpenMobileSidebar}
        ref={mobileSidebarButtonRef}
        aria-label="Open fleet agents"
      >
        <Menu className="size-4" />
      </Button>

      {!settings && agent ? (
        <Link to={agentTabPath(agent.id, "chat")} aria-label={`Chat with ${agentTitle}`}
          className="flex min-h-11 min-w-0 flex-1 items-center rounded-sm px-1 outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/35">
          <div className="flex min-w-0 flex-col justify-center gap-0.5">
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

        </Link>
      ) : <div className="min-w-0 flex-1 truncate text-sm font-semibold">{agentTitle}</div>}

      {!settings && agent ? (
        <TooltipProvider delayDuration={200}>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button asChild variant={activeTab === "runtime" ? "secondary" : "ghost"} size="sm" className="size-11 shrink-0 px-0 sm:w-auto sm:gap-2 sm:px-3">
                <Link to={agentTabPath(agent.id, "runtime")} aria-label="Runtime" aria-current={activeTab === "runtime" ? "page" : undefined}>
                  <SlidersHorizontal className="size-4" aria-hidden="true" />
                  <span className="hidden sm:inline">Runtime</span>
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Runtime</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                asChild
                variant={activeTab === "chat" && !threadID ? "secondary" : "ghost"}
                size="sm"
                className="size-11 shrink-0 px-0 sm:w-auto sm:gap-2 sm:px-3"
              >
                <Link
                  to={`${agentTabPath(agent.id, "chat")}/threads`}
                  aria-label="Thread Explorer"
                  aria-current={activeTab === "chat" && !threadID ? "page" : undefined}
                >
                  <MessagesSquare className="size-4" aria-hidden="true" />
                  <span className="hidden sm:inline">Threads</span>
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Thread Explorer</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : null}
    </header>
  );
}
