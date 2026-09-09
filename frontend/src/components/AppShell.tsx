import { useModuleFilePanel } from "@/modules/use-module-file-panel";
import { ThreadModulesProvider, useThreadModuleSubscription } from "@/hooks/use-thread-modules";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  Link,
  Outlet,
  useLocation,
  useMatch,
  useNavigate,
} from "react-router-dom";
import { AlertTriangle, PanelRightOpen, Plus } from "lucide-react";

import {
  listAgents,
  runAgentAction,
  subscribeAgentResourceEvents,
  subscribeFleetEvents,
} from "@/api";
import { ThreadSidebar } from "@/components/thread/ThreadSidebar";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { FleetAgentProvider } from "@/components/fleet/FleetAgentContext";
import { FleetSidebar } from "@/components/fleet/FleetSidebar";
import { FleetStageHeader } from "@/components/fleet/FleetStageHeader";
import {
  agentTabFromPath,
  agentTabPath,
  agentVisualState,
  nextAgentLifecycleAction,
  resolveAgentSelection,
} from "@/lib/fleet-shell";
import { AgentViewModelStore } from "@/lib/agent-view-model-store";
import type { AgentStatus } from "@/types";
import type { AgentResourceName } from "@/types";

const WORKSPACE_DOCK_QUERY = "(min-width: 1280px)";
const MOBILE_SIDEBAR_QUERY = "(max-width: 759px)";
const LAST_AGENT_KEY = "juex:fleet:last-agent";
const SIDEBAR_COLLAPSED_KEY = "juex:fleet:sidebar-collapsed";
const INITIAL_RESOURCE_REVISION: Record<AgentResourceName, number> = {
  workspace: 0,
  observables: 0,
  runtime: 0,
};

type ShellTitleContextValue = {
  setShellHeader: (header: ShellHeaderState) => void;
};

type ShellHeaderState = {
  pathname: string;
  title: string | null;
  updatedAt?: string | null;
  threadStatus?: "Idle" | "Working" | "Failed" | "Archived" | "Unknown";
};

const ShellTitleContext = createContext<ShellTitleContextValue | null>(null);

export function useShellTitle(
  title: string | null,
  updatedAt: string | null = null,
  threadStatus?: ShellHeaderState["threadStatus"],
) {
  const context = useContext(ShellTitleContext);
  const { pathname } = useLocation();

  useEffect(() => {
    context?.setShellHeader({ pathname, title, updatedAt, threadStatus });
  }, [context, pathname, title, updatedAt, threadStatus]);

  useEffect(() => {
    return () => context?.setShellHeader({ pathname, title: null });
  }, [context, pathname]);
}

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const agentMatch = useMatch("/agents/:agentId/*");
  const threadMatch = useMatch("/agents/:agentId/threads/:threadId");
  const agentId = agentMatch?.params.agentId ?? "";
  const threadID = threadMatch?.params.threadId ?? "";
  const settings = location.pathname === "/settings";
  const activeTab = agentTabFromPath(location.pathname);
  const workspaceDocked = useMediaQuery(WORKSPACE_DOCK_QUERY);
  const mobileSidebar = useMediaQuery(MOBILE_SIDEBAR_QUERY);
  const [rosterAgents, setAgents] = useState<AgentStatus[]>([]);
  const [statusStore] = useState(() => new AgentViewModelStore());
  const statusRevision = useSyncExternalStore(
    statusStore.subscribe,
    statusStore.getRevision,
    statusStore.getRevision,
  );
  const agents = useMemo(() => {
    void statusRevision;
    return statusStore.projectAgents(rosterAgents);
  }, [rosterAgents, statusRevision, statusStore]);
  const [agentsLoaded, setAgentsLoaded] = useState(false);
  const [busyAgentID, setBusyAgentID] = useState<string | null>(null);
  const [fleetError, setFleetError] = useState<string | null>(null);
  const [rosterError, setRosterError] = useState<string | null>(null);
  const [resourceRevision, setResourceRevision] = useState(
    INITIAL_RESOURCE_REVISION,
  );
  const rosterRevision = useRef(0);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(
    () => window.localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "true",
  );
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const [shellHeader, setShellHeader] = useState<ShellHeaderState | null>(null);
  const [workspaceDockOpen, setWorkspaceDockOpen] = useState(true);
  const [workspaceSheetOpen, setWorkspaceSheetOpen] = useState(false);
  const sidebarEntry = useRef<HTMLButtonElement>(null);
  const currentAgent =
    agents.find((candidate) => candidate.id === agentId) ?? null;
  const invalidAgentRoute =
    agentsLoaded && agentId !== "" && currentAgent === null;

  const refreshAgents = useCallback(async () => {
    const requestedAt = rosterRevision.current;
    try {
      const next = await listAgents();
      if (requestedAt !== rosterRevision.current) return;
      statusStore.seedAgents(next);
      setAgents(next);
      setRosterError(null);
      setAgentsLoaded(true);
    } catch (cause) {
      if (requestedAt !== rosterRevision.current) return;
      setRosterError(
        cause instanceof Error ? cause.message : "Failed to load fleet agents.",
      );
    }
  }, [statusStore]);

  useEffect(() => {
    void refreshAgents();
  }, [refreshAgents]);

  useEffect(
    () =>
      subscribeFleetEvents({
        onEvent: (event) => {
          if (event.type === "agent.status") {
            statusStore.applyFleetEvent(event);
            return;
          }
          if (event.type === "fleet.status") return;
          if (event.type === "fleet.roster.unavailable") {
            rosterRevision.current += 1;
            setRosterError(event.error || "Fleet roster is unavailable.");
            return;
          }
          if (event.type === "agent.process") {
            setAgents((current) =>
              current.map((agent) =>
                agent.id === event.agent_id
                  ? { ...agent, process: event.process ?? undefined }
                  : agent,
              ),
            );
            return;
          }
          rosterRevision.current += 1;
          statusStore.seedAgents(event.agents);
          setAgents(event.agents);
          setRosterError(null);
          setAgentsLoaded(true);
        },
        onError: (event) => console.error("fleet status stream failed", event),
      }),
    [statusStore],
  );

  useEffect(() => {
    setResourceRevision(INITIAL_RESOURCE_REVISION);
    if (!agentId || currentAgent?.runtime_health !== "healthy") return;
    return subscribeAgentResourceEvents({
      onEvent: (event) => {
        setResourceRevision((current) => {
          const next = { ...current };
          for (const resource of event.resources) next[resource] += 1;
          return next;
        });
      },
      onError: (event) => console.error("agent resource stream failed", event),
    });
  }, [agentId, currentAgent?.runtime_health]);

  useEffect(() => {
    if (!agentsLoaded || location.pathname !== "/" || agents.length === 0) {
      return;
    }
    const selected = resolveAgentSelection(
      agents,
      window.localStorage.getItem(LAST_AGENT_KEY),
    );
    if (selected) {
      navigate(agentTabPath(selected, "chat"), { replace: true });
    }
  }, [agents, agentsLoaded, location.pathname, navigate]);

  useEffect(() => {
    if (!invalidAgentRoute) return;
    const selected = resolveAgentSelection(
      agents,
      window.localStorage.getItem(LAST_AGENT_KEY),
    );
    if (selected) {
      navigate(agentTabPath(selected, "chat"), { replace: true });
    } else {
      navigate("/", { replace: true });
    }
  }, [agents, invalidAgentRoute, navigate]);

  useEffect(() => {
    if (currentAgent) {
      window.localStorage.setItem(LAST_AGENT_KEY, currentAgent.id);
    }
  }, [currentAgent]);

  useEffect(() => {
    window.localStorage.setItem(
      SIDEBAR_COLLAPSED_KEY,
      String(sidebarCollapsed),
    );
  }, [sidebarCollapsed]);

  useEffect(() => {
    if (!mobileSidebar) setMobileSidebarOpen(false);
  }, [mobileSidebar]);

  useEffect(() => {
    if (activeTab !== "chat") {
      setWorkspaceSheetOpen(false);
    }
  }, [activeTab]);

  const moduleState = useThreadModuleSubscription(threadID);
  const runLifecycle = useCallback(
    async (agent: AgentStatus) => {
      const action = nextAgentLifecycleAction(agent);
      setBusyAgentID(agent.id);
      setFleetError(null);
      try {
        await runAgentAction(agent.id, action);
        await refreshAgents();
      } catch (cause) {
        const actionError =
          cause instanceof Error
            ? cause.message
            : `Failed to ${action} ${agent.name || agent.id}.`;
        await refreshAgents();
        setFleetError(actionError);
      } finally {
        setBusyAgentID(null);
      }
    },
    [refreshAgents],
  );

  const startCurrentAgent = useCallback(async () => {
    if (!currentAgent) return;
    await runLifecycle(currentAgent);
  }, [currentAgent, runLifecycle]);

  const runtimeContext = useMemo(
    () => ({
      agent: currentAgent,
      agents,
      agentsLoaded,
      statusStore,
      lifecycleBusy: busyAgentID === currentAgent?.id,
      resourceRevision,
      startAgent: startCurrentAgent,
    }),
    [
      agents,
      agentsLoaded,
      busyAgentID,
      currentAgent,
      resourceRevision,
      startCurrentAgent,
      statusStore,
    ],
  );
  const shellTitleContext = useMemo<ShellTitleContextValue>(
    () => ({ setShellHeader }),
    [],
  );

  const { title: filePanelTitle, rootKey: filePanelKey, ...filePanelProps } = useModuleFilePanel({
    agentID: agentId, threadID, snapshot: moduleState.snapshot,
    workspaceHealthy: currentAgent?.runtime_health === "healthy",
    workspaceRevision: resourceRevision.workspace,
  });
  const workspaceAvailable = Boolean(currentAgent) && activeTab === "chat" && !settings;
  const workspaceOpen = workspaceDocked
    ? workspaceDockOpen && workspaceAvailable
    : workspaceSheetOpen && workspaceAvailable;

  const sidebar = (
    <FleetSidebar
      agents={agents}
      selectedAgentID={agentId}
      busyAgentID={busyAgentID}
      collapsed={sidebarCollapsed}
      mobile={mobileSidebar}
      onCollapse={() => setSidebarCollapsed(true)}
      onExpand={() => setSidebarCollapsed(false)}
      onNavigate={() => setMobileSidebarOpen(false)}
      onToggleLifecycle={(agent) => void runLifecycle(agent)}
    />
  );
  const emptyFleet =
    agentsLoaded && agents.length === 0 && location.pathname === "/";
  const failedAgent =
    currentAgent && agentVisualState(currentAgent) === "failed"
      ? currentAgent
      : null;

  return (
    <ShellTitleContext.Provider value={shellTitleContext}>
      <FleetAgentProvider value={runtimeContext}>
        <ThreadModulesProvider value={moduleState}>
        <div className="fixed inset-0 flex h-svh min-h-0 overflow-clip bg-background">
          <div className="hidden min-[760px]:flex">{sidebar}</div>
          <Sheet open={mobileSidebarOpen} onOpenChange={setMobileSidebarOpen}>
            <SheetContent
              side="left"
              className="data-[side=left]:w-[min(84vw,268px)] max-w-none gap-0 border-r p-0 min-[760px]:hidden"
            >
              <SheetHeader className="sr-only">
                <SheetTitle>Fleet agents</SheetTitle>
                <SheetDescription>
                  Switch agents and control their runtime.
                </SheetDescription>
              </SheetHeader>
              {sidebar}
            </SheetContent>
          </Sheet>

          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
            <FleetStageHeader
              agent={currentAgent}
              contextTitle={shellHeader?.pathname === location.pathname ? shellHeader.title : null}
              threadStatus={shellHeader?.pathname === location.pathname ? shellHeader.threadStatus : undefined}
              threadID={threadID}
              activeTab={activeTab}
              settings={settings}
              onOpenMobileSidebar={() => setMobileSidebarOpen(true)}

            />
            {failedAgent ? (
              <div
                className="flex shrink-0 items-center gap-2 border-b border-destructive/25 bg-destructive/10 px-4 py-2 text-sm text-destructive"
                role="alert"
              >
                <AlertTriangle className="size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate">
                  {failedAgent.problem || "Agent runtime needs attention."}
                </span>
                <Button asChild variant="outline" size="sm">
                  <Link to={agentTabPath(failedAgent.id, "runtime") + "/logs"}>
                    View logs
                  </Link>
                </Button>
              </div>
            ) : null}
            {(fleetError ?? rosterError) && agentsLoaded ? (
              <div
                className="shrink-0 border-b border-destructive/25 bg-destructive/10 px-4 py-2 text-sm text-destructive"
                role="alert"
              >
                {fleetError ?? rosterError}
              </div>
            ) : null}

            <div className="relative flex min-h-0 min-w-0 flex-1 overflow-hidden">
              <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
                {(fleetError ?? rosterError) && !agentsLoaded ? (
                  <div
                    className="flex min-h-0 flex-1 items-center justify-center px-4 py-8"
                    role="alert"
                  >
                    <div className="w-full max-w-lg rounded-md border border-destructive/35 bg-destructive/10 px-6 py-8 text-center text-destructive">
                      <AlertTriangle className="mx-auto size-5" />
                      <h1 className="mt-3 text-base font-semibold">
                        Fleet roster unavailable
                      </h1>
                      <p className="mt-2 text-sm">{fleetError ?? rosterError}</p>
                      <Button
                        type="button"
                        variant="outline"
                        className="mt-5"
                        onClick={() => void refreshAgents()}
                      >
                        Retry
                      </Button>
                    </div>
                  </div>
                ) : emptyFleet ? (
                  <FleetEmptyState />
                ) : location.pathname === "/" ? (
                  <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
                    Loading fleet...
                  </div>
                ) : invalidAgentRoute ? (
                  <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
                    Loading agent...
                  </div>
                ) : (
                  <Outlet key={agentId || "fleet-settings"} />
                )}
              </div>
              {workspaceDocked && workspaceDockOpen && workspaceAvailable ? (
                <aside aria-label="Thread sidebar" className="h-full w-[clamp(18rem,24vw,22rem)] shrink-0 overflow-hidden border-l bg-card">
                  <ThreadSidebar key={`${agentId}:${threadID}`} agentID={agentId} threadID={threadID}
                    filePanel={{ ...filePanelProps, title: filePanelTitle, rootKey: filePanelKey }}
                    onClose={() => { setWorkspaceDockOpen(false); requestAnimationFrame(() => sidebarEntry.current?.focus()); }} />
                </aside>
              ) : null}
              {workspaceAvailable && !workspaceOpen ? (
                <button ref={sidebarEntry} type="button" aria-label="Open sidebar" title="Status and files"
                  className="absolute right-0 top-1/2 z-20 flex h-14 w-8 -translate-y-1/2 items-center justify-center rounded-l-md border border-r-0 bg-card text-muted-foreground shadow-sm outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35"
                  onClick={() => workspaceDocked ? setWorkspaceDockOpen(true) : setWorkspaceSheetOpen(true)}>
                  <PanelRightOpen className="size-4" />
                </button>
              ) : null}
            </div>
          </div>

          <Sheet
            open={!workspaceDocked && workspaceSheetOpen && workspaceAvailable}
            onOpenChange={setWorkspaceSheetOpen}
          >
            <SheetContent
              className="flex !w-[min(100vw,22rem)] !max-w-none flex-col gap-0 bg-card p-0 xl:hidden"
              side="right" showCloseButton={false}
              onCloseAutoFocus={(event) => { event.preventDefault(); sidebarEntry.current?.focus(); }}
            >
              <SheetHeader className="sr-only">
                <SheetTitle>Thread sidebar</SheetTitle>
                <SheetDescription>Inspect Thread status and browse files.</SheetDescription>
              </SheetHeader>
              <ThreadSidebar key={`${agentId}:${threadID}`} agentID={agentId} threadID={threadID}
                filePanel={{ ...filePanelProps, title: filePanelTitle, rootKey: filePanelKey }}
                onClose={() => setWorkspaceSheetOpen(false)} />
            </SheetContent>
          </Sheet>
        </div>
        </ThreadModulesProvider>
      </FleetAgentProvider>
    </ShellTitleContext.Provider>
  );
}

function FleetEmptyState() {
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-4 py-8">
      <div className="w-full max-w-lg rounded-md border bg-card px-6 py-8 text-center shadow-[var(--shadow-sm)]">
        <h1 className="text-xl font-semibold text-foreground">
          Add your first agent
        </h1>
        <p className="mx-auto mt-2 max-w-sm text-sm text-muted-foreground">
          Register a workspace to open its conversations and runtime controls.
        </p>
        <Button asChild className="mt-5">
          <Link to="/settings?add=1">
            <Plus className="size-4" />
            Add agent
          </Link>
        </Button>
        <div className="mt-4 font-mono text-xs text-muted-foreground">
          juex agent add /absolute/workspace
        </div>
      </div>
    </div>
  );
}

function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onStoreChange) => {
      const media = window.matchMedia(query);
      media.addEventListener("change", onStoreChange);
      return () => media.removeEventListener("change", onStoreChange);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}
