import { useNavigate } from "react-router-dom";
import { useState } from "react";
import { Plus } from "lucide-react";
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
      <SidebarHeader>
        <Button
          onClick={handleNewChat}
          className="w-full justify-start"
          variant="default"
        >
          <Plus className="mr-2 size-4" />
          New chat
        </Button>
      </SidebarHeader>
      <SidebarContent>
        <SidebarSessionList createdSession={createdSession} />
      </SidebarContent>
      <SidebarFooter className="text-muted-foreground text-xs px-3 py-2">
        juex serve
      </SidebarFooter>
    </ShadSidebar>
  );
}
