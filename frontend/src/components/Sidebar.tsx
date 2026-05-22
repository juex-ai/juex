import { useNavigate } from "react-router-dom";
import { useState } from "react";
import { Plus } from "lucide-react";
import { LogoMark } from "@/components/LogoMark";
import { Button } from "@/components/ui/button";
import {
  Sidebar as ShadSidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
} from "@/components/ui/sidebar";
import { SidebarSessionList } from "@/components/SidebarSessionList";
import { createSession } from "@/api";
import type { SessionInfo } from "@/types";

export function Sidebar() {
  const navigate = useNavigate();
  const [createdSession, setCreatedSession] = useState<SessionInfo | null>(null);

  async function handleNewChat() {
    try {
      const r = await createSession();
      setCreatedSession(r);
      navigate(`/sessions/${encodeURIComponent(r.id)}`);
    } catch (e) {
      console.error("createSession failed", e);
    }
  }

  return (
    <ShadSidebar collapsible="offcanvas">
      <SidebarHeader className="gap-3 px-3 pb-2 pt-3">
        <div className="flex items-center gap-2 px-1 text-primary">
          <LogoMark className="size-7" />
          <span className="font-serif text-2xl italic leading-none">juex</span>
        </div>
        <Button
          onClick={handleNewChat}
          className="w-full justify-start gap-2"
          variant="default"
        >
          <Plus className="size-4" />
          New chat
        </Button>
      </SidebarHeader>
      <SidebarContent>
        <SidebarSessionList createdSession={createdSession} />
      </SidebarContent>
      <SidebarFooter className="border-t border-sidebar-border px-4 py-3 font-mono text-[11px] text-muted-foreground">
        <span>juex serve</span>
      </SidebarFooter>
    </ShadSidebar>
  );
}
