import { useNavigate } from "react-router-dom";
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

export function Sidebar() {
  const navigate = useNavigate();

  async function handleNewChat() {
    try {
      const r = await createSession();
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
        <SidebarSessionList />
      </SidebarContent>
      <SidebarFooter className="text-muted-foreground text-xs px-3 py-2">
        juex serve
      </SidebarFooter>
    </ShadSidebar>
  );
}
