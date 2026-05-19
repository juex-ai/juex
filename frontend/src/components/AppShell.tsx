import { Link, Outlet } from "react-router-dom";
import { useEffect, useState } from "react";
import {
  SidebarProvider,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Sidebar } from "@/components/Sidebar";
import { FileTreePanel } from "@/components/FileTreePanel";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { FolderIcon, FolderOpenIcon, Wrench } from "lucide-react";
import { getRuntimeStatus } from "@/api";
import type { RuntimeStatusResponse } from "@/types";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

export function AppShell() {
  const [rightPanelOpen, setRightPanelOpen] = useState(true);
  const [runtimeStatus, setRuntimeStatus] =
    useState<RuntimeStatusResponse | null>(null);

  useEffect(() => {
    let live = true;
    const refreshRuntimeStatus = () => {
      getRuntimeStatus()
        .then((status) => {
          if (live) setRuntimeStatus(status);
        })
        .catch((e) => console.error("getRuntimeStatus failed", e));
    };
    refreshRuntimeStatus();
    const interval = window.setInterval(refreshRuntimeStatus, 30_000);
    return () => {
      live = false;
      window.clearInterval(interval);
    };
  }, []);

  const mcpErrorCount = runtimeStatus?.mcp.errors ?? 0;
  const mcpConnectedLabel = runtimeStatus
    ? `MCP ${runtimeStatus.mcp.connected}/${runtimeStatus.mcp.configured} connected`
    : "";
  const mcpLabel = runtimeStatus
    ? mcpErrorCount > 0
      ? `${mcpConnectedLabel}, ${mcpErrorCount} ${
          mcpErrorCount === 1 ? "error" : "errors"
        }`
      : runtimeStatus.mcp.connected > 0
        ? mcpConnectedLabel
        : runtimeStatus.mcp.configured > 0
          ? `MCP not started (${runtimeStatus.mcp.configured})`
          : "MCP 0 configured"
    : "";

  return (
    <SidebarProvider className="h-svh min-h-0 overflow-hidden">
      <Sidebar />
      <SidebarInset className="min-h-0 flex flex-row">
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden relative">
          <header className="flex h-12 shrink-0 items-center justify-between border-b px-4">
            <div className="flex items-center gap-2">
              <SidebarTrigger className="-ml-1" />
              <span className="font-semibold">juex</span>
              {runtimeStatus ? (
                <div className="flex items-center gap-1">
                  <Badge
                    variant={mcpErrorCount > 0 ? "destructive" : "outline"}
                    className="font-mono text-xs"
                  >
                    {mcpLabel}
                  </Badge>
                  <Badge variant="outline" className="font-mono text-xs">
                    SKILLS {runtimeStatus.skills.count}
                  </Badge>
                </div>
              ) : null}
            </div>
            <div className="flex items-center gap-1">
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      asChild
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-foreground"
                    >
                      <Link to="/runtime" aria-label="Runtime details">
                        <Wrench className="size-4" />
                      </Link>
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Runtime details</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-foreground"
                      onClick={() => setRightPanelOpen(!rightPanelOpen)}
                      aria-label={rightPanelOpen ? "Hide workspace" : "Show workspace"}
                    >
                      {rightPanelOpen ? (
                        <FolderOpenIcon className="size-4" />
                      ) : (
                        <FolderIcon className="size-4" />
                      )}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>
                    {rightPanelOpen ? "Hide workspace" : "Show workspace"}
                  </TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </header>
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <Outlet />
          </div>
        </div>
        {rightPanelOpen && (
          <div className="flex h-full w-72 flex-shrink-0 flex-col overflow-hidden border-l bg-sidebar transition-all">
            <FileTreePanel />
          </div>
        )}
      </SidebarInset>
    </SidebarProvider>
  );
}
