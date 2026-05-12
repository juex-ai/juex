import { Outlet } from "react-router-dom";
import { useState } from "react";
import {
  SidebarProvider,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Sidebar } from "@/components/Sidebar";
import { FileTreePanel } from "@/components/FileTreePanel";
import { Button } from "@/components/ui/button";
import { FolderIcon, FolderOpenIcon } from "lucide-react";

export function AppShell() {
  const [rightPanelOpen, setRightPanelOpen] = useState(true);

  return (
    <SidebarProvider className="h-svh min-h-0 overflow-hidden">
      <Sidebar />
      <SidebarInset className="min-h-0 flex flex-row">
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden relative">
          <header className="flex h-12 shrink-0 items-center justify-between border-b px-4">
            <div className="flex items-center gap-2">
              <SidebarTrigger className="-ml-1" />
              <span className="font-semibold">juex</span>
            </div>
            <Button
              variant="ghost"
              size="icon"
              className="text-muted-foreground hover:text-foreground"
              onClick={() => setRightPanelOpen(!rightPanelOpen)}
            >
              {rightPanelOpen ? (
                <FolderOpenIcon className="w-4 h-4" />
              ) : (
                <FolderIcon className="w-4 h-4" />
              )}
            </Button>
          </header>
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <Outlet />
          </div>
        </div>
        {rightPanelOpen && (
          <div className="w-72 border-l bg-sidebar flex-shrink-0 flex flex-col h-full overflow-hidden transition-all">
             <FileTreePanel />
          </div>
        )}
      </SidebarInset>
    </SidebarProvider>
  );
}
