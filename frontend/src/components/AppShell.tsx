import { Outlet } from "react-router-dom";
import {
  SidebarProvider,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Sidebar } from "@/components/Sidebar";

export function AppShell() {
  return (
    <SidebarProvider className="h-svh min-h-0 overflow-hidden">
      <Sidebar />
      <SidebarInset className="min-h-0">
        <header className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger className="-ml-1" />
          <span className="font-semibold">juex</span>
        </header>
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
          <Outlet />
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
