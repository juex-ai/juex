import { Link, Outlet, useLocation, useNavigate } from "react-router-dom";
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from "react";
import { FileTreePanel } from "@/components/FileTreePanel";
import { LogoMark } from "@/components/LogoMark";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  ArrowLeft,
  FolderIcon,
  FolderOpenIcon,
  HistoryIcon,
  Wrench,
} from "lucide-react";
import { getRuntimeStatus } from "@/api";
import type { RuntimeStatusResponse } from "@/types";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { isHistoryPath } from "@/lib/route-state";
import {
  formatShellUpdatedAt,
  shellMCPBadge,
  shellUpdatedAtClassName,
  type ShellMCPTone,
} from "@/lib/shell-header";
import { cn } from "@/lib/utils";

const WORKSPACE_DOCK_QUERY = "(min-width: 1280px)";

type ShellTitleContextValue = {
  setShellHeader: (header: ShellHeaderState) => void;
};

type ShellHeaderState = {
  title: string | null;
  updatedAt?: string | null;
};

const ShellTitleContext = createContext<ShellTitleContextValue | null>(null);

export function useShellTitle(
  title: string | null,
  updatedAt: string | null = null,
) {
  const context = useContext(ShellTitleContext);

  useEffect(() => {
    context?.setShellHeader({ title, updatedAt });
    return () => context?.setShellHeader({ title: null, updatedAt: null });
  }, [context, title, updatedAt]);
}

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const workspaceDocked = useMediaQuery(WORKSPACE_DOCK_QUERY);
  const [workspaceDockOpen, setWorkspaceDockOpen] = useState(true);
  const [workspaceSheetOpen, setWorkspaceSheetOpen] = useState(false);
  const [shellHeader, setShellHeader] = useState<ShellHeaderState>({
    title: null,
    updatedAt: null,
  });
  const [lastContentPath, setLastContentPath] = useState(
    () => window.sessionStorage.getItem("juex:last-content-path") || "/",
  );
  const [runtimeStatus, setRuntimeStatus] =
    useState<RuntimeStatusResponse | null>(null);

  useEffect(() => {
    if (location.pathname === "/runtime") return;
    const next = `${location.pathname}${location.search}${location.hash}`;
    setLastContentPath(next);
    window.sessionStorage.setItem("juex:last-content-path", next);
  }, [location.hash, location.pathname, location.search]);

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

  const shellTitle = shellHeader.title;
  const shellUpdatedAt = formatShellUpdatedAt(shellHeader.updatedAt);
  const mcpBadge = runtimeStatus ? shellMCPBadge(runtimeStatus.mcp) : null;
  const workspaceOpen = workspaceDocked ? workspaceDockOpen : workspaceSheetOpen;
  const workspaceLabel = workspaceDocked
    ? workspaceOpen
      ? "Hide workspace"
      : "Show workspace"
    : "Open workspace";
  const toggleWorkspace = () => {
    if (workspaceDocked) {
      setWorkspaceDockOpen((open) => !open);
    } else {
      setWorkspaceSheetOpen(true);
    }
  };
  const shellContextValue = useMemo(() => ({ setShellHeader }), []);
  const onRuntimePage = location.pathname === "/runtime";
  const onHistoryPage = isHistoryPath(location.pathname);

  return (
    <ShellTitleContext.Provider value={shellContextValue}>
      <div className="flex h-svh min-h-0 overflow-hidden bg-background">
        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          <header className="flex h-[var(--juex-header-height)] shrink-0 items-center gap-2 border-b bg-card px-4 shadow-[var(--shadow-xs)]">
            <Link
              to="/"
              className="flex shrink-0 items-center gap-2 text-primary transition-colors hover:text-primary/85"
              aria-label="New chat"
            >
              <LogoMark className="size-6" />
              <span className="hidden font-serif text-2xl italic leading-tight sm:inline">
                juex
              </span>
            </Link>
            <div className="min-w-0 flex-1">
              {shellTitle ? (
                <div className="min-w-0 border-l pl-3 text-primary">
                  <span className="block truncate pb-0.5 font-serif text-xl italic leading-tight">
                    {shellTitle}
                  </span>
                </div>
              ) : null}
            </div>
            <div className="ml-auto flex shrink-0 items-center gap-1">
              {runtimeStatus && mcpBadge ? (
                <div className="hidden shrink-0 items-center gap-1 sm:flex">
                  {shellUpdatedAt ? (
                    <span className={shellUpdatedAtClassName()}>
                      {shellUpdatedAt}
                    </span>
                  ) : null}
                  <Badge
                    variant="outline"
                    className="font-mono text-[11px]"
                  >
                    skills {runtimeStatus.skills.count}
                  </Badge>
                  <Badge
                    variant="outline"
                    className="gap-1.5 font-mono text-[11px]"
                    title={mcpBadge.title}
                    aria-label={mcpBadge.title}
                  >
                    {mcpBadge.label}
                    <span
                      aria-hidden="true"
                      className={cn(
                        "size-1.5 rounded-full",
                        mcpToneDotClassName(mcpBadge.tone),
                      )}
                    />
                  </Badge>
                </div>
              ) : null}
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      asChild
                      variant="ghost"
                      size="icon"
                      className={cn(
                        "text-muted-foreground hover:text-foreground",
                        onHistoryPage && "bg-muted text-foreground",
                      )}
                    >
                      <Link to="/history" aria-label="History">
                        <HistoryIcon className="size-4" />
                      </Link>
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>History</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    {onRuntimePage ? (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-muted-foreground hover:text-foreground"
                        aria-label="Back"
                        onClick={() => navigate(lastContentPath || "/")}
                      >
                        <ArrowLeft className="size-4" />
                      </Button>
                    ) : (
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
                    )}
                  </TooltipTrigger>
                  <TooltipContent>
                    {onRuntimePage ? "Back" : "Runtime details"}
                  </TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-foreground"
                      onClick={toggleWorkspace}
                      aria-label={workspaceLabel}
                    >
                      {workspaceOpen ? (
                        <FolderOpenIcon className="size-4" />
                      ) : (
                        <FolderIcon className="size-4" />
                      )}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{workspaceLabel}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </header>
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            <Outlet />
          </div>
        </div>
        {workspaceDockOpen && (
          <div className="hidden h-full w-[clamp(16rem,22vw,20rem)] flex-shrink-0 flex-col overflow-hidden border-l bg-card transition-all xl:flex">
            <FileTreePanel active={workspaceDocked} />
          </div>
        )}
        <Sheet
          open={!workspaceDocked && workspaceSheetOpen}
          onOpenChange={setWorkspaceSheetOpen}
        >
          <SheetContent
            className="flex !w-[min(100vw,22rem)] !max-w-none flex-col gap-0 bg-card p-0 sm:!max-w-md xl:hidden"
            side="right"
          >
            <SheetHeader className="sr-only">
              <SheetTitle>Workspace</SheetTitle>
              <SheetDescription>
                Browse files in the current workspace.
              </SheetDescription>
            </SheetHeader>
            <FileTreePanel active={!workspaceDocked && workspaceSheetOpen} />
          </SheetContent>
        </Sheet>
      </div>
    </ShellTitleContext.Provider>
  );
}

function mcpToneDotClassName(tone: ShellMCPTone) {
  if (tone === "ok") return "bg-emerald-500";
  if (tone === "error") return "bg-destructive";
  return "bg-muted-foreground/45";
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
