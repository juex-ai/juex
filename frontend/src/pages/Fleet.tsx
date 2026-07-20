import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  Check,
  ChevronRight,
  Circle,
  CircleCheck,
  CircleOff,
  FileCog,
  Folder,
  FolderOpen,
  Play,
  Plus,
  RefreshCw,
  RotateCw,
  ScrollText,
  Square,
  Trash2,
} from "lucide-react";

import {
  addAgent,
  createDirectory,
  listAgents,
  listDirectories,
  removeAgent,
  runAgentAction,
  setAgentEnabled,
} from "@/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  directoryCreateKeyAction,
  mergeCreatedDirectory,
  revealScrollableTail,
  revealWorkspaceSelectionTail,
  shouldApplyDirectoryBrowseResult,
  shouldApplyDirectoryCreateResult,
  validateNewDirectoryName,
  workspacePathUpdate,
} from "@/lib/fleet-directories";
import { agentPagePath } from "@/lib/fleet-routes";
import {
  agentActionWarning,
  agentStateLabel,
  agentVisualState,
  nextAgentLifecycleAction,
} from "@/lib/fleet-shell";
import { cn } from "@/lib/utils";
import type { AgentStatus, DirectoryListing } from "@/types";

type LifecycleAction = "start" | "stop" | "restart";

const FLEET_ROSTER_GRID_CLASS =
  "grid grid-cols-[minmax(13rem,1fr)_minmax(18rem,1.4fr)_8rem_15rem]";

export function Fleet() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [agents, setAgents] = useState<AgentStatus[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busyAgent, setBusyAgent] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<AgentStatus | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async ({ quiet = false } = {}) => {
    if (!quiet) setRefreshing(true);
    setError(null);
    try {
      setAgents(await listAgents());
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Failed to load fleet roster.",
      );
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void refresh({ quiet: true });
    const timer = window.setInterval(() => void refresh({ quiet: true }), 10_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    if (searchParams.get("add") === "1") {
      setAddOpen(true);
    }
  }, [searchParams]);

  function setAddDialogOpen(open: boolean) {
    setAddOpen(open);
    if (!open && searchParams.has("add")) {
      const next = new URLSearchParams(searchParams);
      next.delete("add");
      setSearchParams(next, { replace: true });
    }
  }

  async function runAction(agent: AgentStatus, action: LifecycleAction) {
    setBusyAgent(agent.id);
    setError(null);
    try {
      const next = await runAgentAction(agent.id, action);
      replaceAgent(next);
      setError(agentActionWarning(action, next));
    } catch (cause) {
      const actionError =
        cause instanceof Error
          ? cause.message
          : `Failed to ${action} ${agent.name || agent.id}.`;
      await refresh({ quiet: true });
      setError(actionError);
    } finally {
      setBusyAgent(null);
    }
  }

  async function toggleEnabled(agent: AgentStatus) {
    setBusyAgent(agent.id);
    setError(null);
    try {
      replaceAgent(await setAgentEnabled(agent.id, !agent.enabled));
    } catch (cause) {
      const actionError =
        cause instanceof Error
          ? cause.message
          : `Failed to ${agent.enabled ? "disable" : "enable"} ${agent.name || agent.id}.`;
      await refresh({ quiet: true });
      setError(actionError);
    } finally {
      setBusyAgent(null);
    }
  }

  function replaceAgent(next: AgentStatus) {
    setAgents((current) => {
      const exists = current.some((item) => item.id === next.id);
      const nextList = exists
        ? current.map((item) => (item.id === next.id ? next : item))
        : [...current, next];
      return nextList.sort((a, b) => a.id.localeCompare(b.id));
    });
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-background">
      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-7xl flex-col gap-4 px-4 py-6 md:px-6">
          <div className="flex items-start justify-between gap-3">
            <div>
              <h1 className="text-xl font-semibold text-foreground">
                Fleet settings
              </h1>
              <p className="mt-1 text-sm text-muted-foreground">
                Fleet service details and registered agent workspaces.
              </p>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              <Button type="button" size="sm" onClick={() => setAddDialogOpen(true)}>
                <Plus className="size-3.5" />
                Add agent
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-foreground"
                onClick={() => void refresh()}
                disabled={refreshing}
                aria-label="Refresh fleet"
                title="Refresh fleet"
              >
                <RefreshCw
                  className={cn(
                    "size-4",
                    refreshing && "animate-spin motion-reduce:animate-none",
                  )}
                />
              </Button>
            </div>
          </div>

          {error ? (
            <div
              role="alert"
              className="rounded-md border border-destructive/35 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {error}
            </div>
          ) : null}

          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <FleetSettingCard
              label="Fleet server"
              value={window.location.host}
              detail="Reachable from this browser"
            />
            <FleetSettingCard
              label="System service"
              value="Not reported"
              detail={`${agents.filter((agent) => agent.runtime_health === "healthy").length} agent runtimes connected; service-manager state is unavailable`}
            />
            <FleetSettingCard
              label="Models & providers"
              value="Per agent"
              detail="Open an agent Runtime tab for resolved details"
            />
            <FleetSettingCard
              label="Extensions"
              value="Per workspace"
              detail="Loaded by each registered agent runtime"
            />
          </div>

          <div>
            <h2 className="text-sm font-semibold text-foreground">Agents</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              Lifecycle, registration, logs, and workspace configuration.
            </p>
          </div>

          <div className="overflow-x-auto rounded-md border bg-card shadow-[var(--shadow-xs)]">
            <div className="min-w-[54rem]">
              <div
                className={cn(
                  FLEET_ROSTER_GRID_CLASS,
                  "bg-muted/60 text-[11px] uppercase tracking-[0.12em] text-muted-foreground",
                )}
              >
                <div className="px-3 py-2 font-medium">Agent</div>
                <div className="px-3 py-2 font-medium">Workspace</div>
                <div className="px-3 py-2 font-medium">State</div>
                <div className="px-3 py-2 text-right font-medium">Actions</div>
              </div>
              {loading ? (
                <div className="px-4 py-8 text-sm text-muted-foreground">
                  Loading agents...
                </div>
              ) : agents.length === 0 ? (
                <div className="px-4 py-10 text-center text-sm text-muted-foreground">
                  No registered agents.
                </div>
              ) : (
                <div className="divide-y">
                  {agents.map((agent) => (
                    <AgentRow
                      key={agent.id}
                      agent={agent}
                      busy={busyAgent === agent.id}
                      onAction={(action) => void runAction(agent, action)}
                      onToggleEnabled={() => void toggleEnabled(agent)}
                      onRemove={() => setRemoveTarget(agent)}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      </main>

      <AddAgentDialog
        open={addOpen}
        onOpenChange={setAddDialogOpen}
        onAdded={(agent) => replaceAgent(agent)}
        onPartialFailure={() => refresh({ quiet: true })}
      />
      <RemoveAgentDialog
        agent={removeTarget}
        onOpenChange={(open) => {
          if (!open) setRemoveTarget(null);
        }}
        onRemoved={(agent) => {
          setAgents((current) => current.filter((item) => item.id !== agent.id));
          setRemoveTarget(null);
        }}
        onPartialFailure={() => refresh({ quiet: true })}
      />
    </div>
  );
}

function FleetSettingCard({
  label,
  value,
  detail,
}: {
  label: string;
  value: string;
  detail: string;
}) {
  return (
    <div className="rounded-md border bg-card px-3 py-3 shadow-[var(--shadow-xs)]">
      <div className="text-[11px] font-medium uppercase text-muted-foreground">
        {label}
      </div>
      <div className="mt-1 truncate font-mono text-sm text-foreground" title={value}>
        {value}
      </div>
      <div className="mt-1 text-xs text-muted-foreground">{detail}</div>
    </div>
  );
}

function AgentRow({
  agent,
  busy,
  onAction,
  onToggleEnabled,
  onRemove,
}: {
  agent: AgentStatus;
  busy: boolean;
  onAction: (action: LifecycleAction) => void;
  onToggleEnabled: () => void;
  onRemove: () => void;
}) {
  const base = agentPagePath(agent.id);
  const lifecycleAction = nextAgentLifecycleAction(agent);
  const visualState = agentVisualState(agent);
  return (
    <div
      className={cn(
        FLEET_ROSTER_GRID_CLASS,
        "items-center text-sm transition-colors",
        !agent.enabled && "bg-muted/25",
      )}
    >
      <div className="min-w-0 px-3 py-3">
        <Link
          to={base}
          className={cn(
            "block truncate font-medium text-foreground outline-none hover:text-primary focus-visible:ring-2 focus-visible:ring-ring/35",
            !agent.enabled && "text-muted-foreground",
          )}
        >
          {agent.name || agent.id}
        </Link>
        <div className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
          {agent.id}
        </div>
        {agent.problem ? (
          <div className="mt-1 line-clamp-2 text-xs text-destructive" title={agent.problem}>
            {agent.problem}
          </div>
        ) : null}
      </div>
      <div
        className="truncate px-3 py-3 font-mono text-xs text-muted-foreground"
        title={agent.workspace || "Workspace unavailable"}
      >
        {agent.workspace || "-"}
      </div>
      <div className="px-3 py-3">
        <Badge
          variant="outline"
          className={cn(
            "font-mono text-[10px]",
            (visualState === "idle" || visualState === "working") &&
              "border-juex-done/35 text-juex-done",
            visualState === "failed" &&
              "border-destructive/35 text-destructive",
            visualState === "stopped" &&
              "border-border text-muted-foreground",
          )}
        >
          {agentStateLabel(agent)}
        </Badge>
        <div className="mt-1 font-mono text-[10px] text-muted-foreground">
          {agent.binding}
        </div>
      </div>
      <div className="flex items-center justify-end gap-1 px-3 py-3">
        <TooltipProvider delayDuration={250}>
          <AgentAction
            label={
              lifecycleAction === "start" ? "Start agent" : "Stop agent"
            }
            disabled={
              busy || (lifecycleAction === "start" && !agent.enabled)
            }
            onClick={() => onAction(lifecycleAction)}
            icon={
              lifecycleAction === "start" ? (
                <Play className="size-3.5" />
              ) : (
                <Square className="size-3.5" />
              )
            }
          />
          <AgentAction
            label="Restart agent"
            disabled={
              busy || !agent.enabled || agent.runtime_health === "stopped"
            }
            onClick={() => onAction("restart")}
            icon={
              <RotateCw
                className={cn(
                  "size-3.5",
                  busy && "animate-spin motion-reduce:animate-none",
                )}
              />
            }
          />
          <AgentAction
            label={agent.enabled ? "Disable agent" : "Enable agent"}
            disabled={busy}
            onClick={onToggleEnabled}
            icon={
              agent.enabled ? (
                <CircleOff className="size-3.5" />
              ) : (
                <CircleCheck className="size-3.5" />
              )
            }
          />
          <AgentLink label="View logs" to={`${base}/logs`} icon={<ScrollText className="size-3.5" />} />
          <AgentLink label="Edit config" to={`${base}/config`} icon={<FileCog className="size-3.5" />} />
          <AgentAction
            label="Remove agent"
            disabled={busy}
            onClick={onRemove}
            destructive
            icon={<Trash2 className="size-3.5" />}
          />
        </TooltipProvider>
      </div>
    </div>
  );
}

function AddAgentDialog({
  open,
  onOpenChange,
  onAdded,
  onPartialFailure,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: (agent: AgentStatus) => void;
  onPartialFailure: () => Promise<void>;
}) {
  const [workspace, setWorkspace] = useState("");
  const [name, setName] = useState("");
  const [autostart, setAutostart] = useState(false);
  const [startNow, setStartNow] = useState(false);
  const [showHidden, setShowHidden] = useState(false);
  const [listing, setListing] = useState<DirectoryListing | null>(null);
  const [browsing, setBrowsing] = useState(false);
  const [directoryDraftOpen, setDirectoryDraftOpen] = useState(false);
  const [directoryName, setDirectoryName] = useState("");
  const [directoryCreating, setDirectoryCreating] = useState(false);
  const [directoryError, setDirectoryError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const workspaceInputRef = useRef<HTMLInputElement>(null);
  const breadcrumbRef = useRef<HTMLDivElement>(null);
  const directoryInputRef = useRef<HTMLInputElement>(null);
  const createDirectoryButtonRef = useRef<HTMLButtonElement>(null);
  const browseGenerationRef = useRef(0);
  const createGenerationRef = useRef(0);
  const directoryCreatingRef = useRef(false);
  const dialogOpenRef = useRef(open);
  const draftOpenRef = useRef(directoryDraftOpen);
  const listingRef = useRef(listing);

  const setWorkspacePath = useCallback(
    (path: string, source: "browser" | "manual") => {
      const update = workspacePathUpdate(path, source);
      setWorkspace(update.path);
      if (update.revealTail) {
        window.requestAnimationFrame(() =>
          revealWorkspaceSelectionTail(
            workspaceInputRef.current,
            breadcrumbRef.current,
          ),
        );
      }
    },
    [],
  );

  const browse = useCallback(
    async (path: string | undefined, hidden: boolean) => {
      const requestGeneration = browseGenerationRef.current + 1;
      browseGenerationRef.current = requestGeneration;
      setBrowsing(true);
      setError(null);
      const resultStillApplies = () =>
        shouldApplyDirectoryBrowseResult({
          requestGeneration,
          currentGeneration: browseGenerationRef.current,
          dialogOpen: dialogOpenRef.current,
        });
      try {
        const next = await listDirectories(path, hidden);
        if (!resultStillApplies()) return;
        setListing(next);
        listingRef.current = next;
        setWorkspacePath(next.path, "browser");
      } catch (cause) {
        if (!resultStillApplies()) return;
        setListing(null);
        listingRef.current = null;
        setError(
          cause instanceof Error ? cause.message : "Failed to browse directories.",
        );
      } finally {
        if (resultStillApplies()) setBrowsing(false);
      }
    },
    [setWorkspacePath],
  );

  useEffect(() => {
    dialogOpenRef.current = open;
    browseGenerationRef.current += 1;
    createGenerationRef.current += 1;
    draftOpenRef.current = false;
    setDirectoryDraftOpen(false);
    setDirectoryName("");
    directoryCreatingRef.current = false;
    setDirectoryCreating(false);
    setDirectoryError(null);
    setBrowsing(false);
    if (!open) return;
    setWorkspace("");
    setName("");
    setAutostart(false);
    setStartNow(false);
    setShowHidden(false);
    setListing(null);
    listingRef.current = null;
    setError(null);
    void browse(undefined, false);
  }, [open, browse]);

  useEffect(() => {
    if (!listing?.path) return;
    window.requestAnimationFrame(() =>
      revealScrollableTail(breadcrumbRef.current),
    );
  }, [listing?.path]);

  function invalidateDirectoryCreate(returnFocus = false) {
    createGenerationRef.current += 1;
    draftOpenRef.current = false;
    setDirectoryDraftOpen(false);
    setDirectoryName("");
    directoryCreatingRef.current = false;
    setDirectoryCreating(false);
    setDirectoryError(null);
    if (returnFocus) {
      window.requestAnimationFrame(() =>
        createDirectoryButtonRef.current?.focus(),
      );
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    dialogOpenRef.current = nextOpen;
    if (!nextOpen) {
      browseGenerationRef.current += 1;
      setBrowsing(false);
      invalidateDirectoryCreate();
    }
    onOpenChange(nextOpen);
  }

  function beginDirectoryCreate() {
    if (!listing) return;
    createGenerationRef.current += 1;
    draftOpenRef.current = true;
    setDirectoryDraftOpen(true);
    setDirectoryName("");
    directoryCreatingRef.current = false;
    setDirectoryCreating(false);
    setDirectoryError(null);
    window.requestAnimationFrame(() => directoryInputRef.current?.focus());
  }

  async function submitDirectoryCreate() {
    if (!listing || directoryCreatingRef.current) return;
    const validation = validateNewDirectoryName(
      listing,
      directoryName,
      showHidden,
    );
    if (validation.error) {
      setDirectoryError(validation.error);
      return;
    }

    const capturedParent = listing.path;
    const requestGeneration = createGenerationRef.current + 1;
    createGenerationRef.current = requestGeneration;
    directoryCreatingRef.current = true;
    setDirectoryCreating(true);
    setDirectoryError(null);

    const resultStillApplies = () =>
      shouldApplyDirectoryCreateResult({
        requestGeneration,
        currentGeneration: createGenerationRef.current,
        capturedParent,
        currentParent: listingRef.current?.path,
        dialogOpen: dialogOpenRef.current,
        draftOpen: draftOpenRef.current,
      });

    try {
      const created = await createDirectory({
        parent: capturedParent,
        name: validation.name,
      });
      if (!resultStillApplies()) return;

      const currentListing = listingRef.current;
      if (currentListing) {
        const nextListing = mergeCreatedDirectory(
          currentListing,
          capturedParent,
          created,
        );
        listingRef.current = nextListing;
        setListing(nextListing);
      }
      setWorkspacePath(created.path, "browser");
      draftOpenRef.current = false;
      directoryCreatingRef.current = false;
      setDirectoryDraftOpen(false);
      setDirectoryName("");
      setDirectoryCreating(false);
      setDirectoryError(null);
    } catch (cause) {
      if (!resultStillApplies()) return;
      setDirectoryError(
        cause instanceof Error
          ? cause.message
          : "Failed to create directory.",
      );
    } finally {
      if (resultStillApplies()) {
        directoryCreatingRef.current = false;
        setDirectoryCreating(false);
      }
    }
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      const result = await addAgent({
        workspace,
        name: name.trim() || undefined,
        autostart: autostart ? true : undefined,
        start: startNow,
      });
      onAdded(result.agent);
      handleOpenChange(false);
    } catch (cause) {
      await onPartialFailure();
      setError(cause instanceof Error ? cause.message : "Failed to add agent.");
    } finally {
      setSubmitting(false);
    }
  }

  const directoryNavigationLocked = directoryDraftOpen || directoryCreating;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="flex max-h-[calc(100svh-2rem)] flex-col overflow-hidden sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add agent</DialogTitle>
          <DialogDescription>
            Choose or create a workspace on this machine.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex min-h-0 flex-1 flex-col [&_[data-slot=dialog-footer]]:mx-0 [&_[data-slot=dialog-footer]]:mb-0 [&_[data-slot=dialog-footer]]:rounded-none [&_[data-slot=dialog-footer]]:border-0 [&_[data-slot=dialog-footer]]:bg-transparent [&_[data-slot=dialog-footer]]:px-0 [&_[data-slot=dialog-footer]]:pb-0 [&_[data-slot=dialog-footer]]:pt-4"
          onSubmit={(event) => void submit(event)}
        >
          <div className="min-h-0 flex-1 overflow-y-auto pr-1">
            <div className="space-y-4">
              <div className="space-y-1.5">
                <label
                  htmlFor="agent-workspace"
                  className="text-xs font-medium"
                >
                  Workspace path
                </label>
                <div className="flex gap-2">
                  <Input
                    ref={workspaceInputRef}
                    id="agent-workspace"
                    className="font-mono text-xs focus-visible:ring-0 focus-visible:ring-offset-0"
                    value={workspace}
                    onChange={(event) =>
                      setWorkspacePath(event.target.value, "manual")
                    }
                    placeholder="/absolute/path/to/workspace"
                    required
                  />
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => void browse(workspace, showHidden)}
                    disabled={
                      browsing || !workspace || directoryNavigationLocked
                    }
                  >
                    <FolderOpen className="size-3.5" />
                    Browse
                  </Button>
                </div>
              </div>

              <div className="overflow-hidden rounded-md border">
                <div className="flex min-h-9 items-center gap-1 border-b bg-muted/45 px-2">
                  <div
                    ref={breadcrumbRef}
                    className="flex min-w-0 flex-1 items-center overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
                  >
                    {listing
                      ? pathBreadcrumbs(listing.path).map((crumb, index) => (
                          <div
                            key={crumb.path}
                            className="flex shrink-0 items-center"
                          >
                            {index > 0 ? (
                              <ChevronRight className="size-3 text-muted-foreground" />
                            ) : null}
                            <button
                              type="button"
                              className="cursor-pointer font-mono text-xs text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:underline disabled:cursor-not-allowed disabled:opacity-50"
                              onClick={() =>
                                void browse(crumb.path, showHidden)
                              }
                              disabled={browsing || directoryNavigationLocked}
                            >
                              {crumb.label}
                            </button>
                          </div>
                        ))
                      : null}
                  </div>
                  <label className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
                    Show hidden
                    <Switch
                      checked={showHidden}
                      disabled={browsing || directoryNavigationLocked}
                      onCheckedChange={(checked) => {
                        setShowHidden(checked);
                        void browse(listing?.path, checked);
                      }}
                      aria-label="Show hidden directories"
                    />
                  </label>
                </div>
                <ScrollArea className="h-52">
                  {browsing ? (
                    <div className="px-3 py-6 text-center text-sm text-muted-foreground">
                      Loading directories...
                    </div>
                  ) : listing ? (
                    <div className="divide-y">
                      {listing.dirs.length === 0 ? (
                        <div className="px-3 py-4 text-center text-sm text-muted-foreground">
                          No subdirectories.
                        </div>
                      ) : null}
                      {listing.dirs.map((directory) => {
                        const selected = workspace === directory.path;
                        return (
                          <div
                            key={directory.path}
                            className={cn(
                              "flex h-10 items-center gap-2 px-2",
                              selected && "bg-sidebar-accent/65",
                            )}
                            data-selected={selected ? "true" : "false"}
                          >
                            <Button
                              type="button"
                              variant="ghost"
                              className="min-w-0 flex-1 justify-start"
                              onClick={() =>
                                void browse(directory.path, showHidden)
                              }
                              title={directory.path}
                              disabled={directoryNavigationLocked}
                            >
                              <Folder className="size-3.5 text-muted-foreground" />
                              <span className="truncate">{directory.name}</span>
                              {directory.registered ? (
                                <Badge
                                  variant="outline"
                                  className="ml-auto text-[10px]"
                                >
                                  Registered
                                </Badge>
                              ) : null}
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="icon-sm"
                              aria-label={`Select ${directory.name}`}
                              title={`Select ${directory.path}`}
                              onClick={() =>
                                setWorkspacePath(directory.path, "browser")
                              }
                              disabled={directoryNavigationLocked}
                              aria-pressed={selected}
                              className={cn(
                                selected &&
                                  "bg-sidebar-accent text-sidebar-accent-foreground",
                              )}
                            >
                              {selected ? (
                                <Check className="size-3.5" />
                              ) : (
                                <Circle className="size-3.5 text-muted-foreground/55" />
                              )}
                            </Button>
                          </div>
                        );
                      })}
                      {directoryDraftOpen ? (
                        <div className="space-y-1.5 px-2 py-2">
                          <div className="flex items-center gap-2">
                            <Folder className="size-3.5 shrink-0 text-muted-foreground" />
                            <label
                              htmlFor="new-directory-name"
                              className="sr-only"
                            >
                              New directory name
                            </label>
                            <Input
                              ref={directoryInputRef}
                              id="new-directory-name"
                              className="h-7 min-w-0 flex-1 focus-visible:ring-0 focus-visible:ring-offset-0 aria-invalid:ring-0"
                              value={directoryName}
                              onChange={(event) => {
                                setDirectoryName(event.target.value);
                                setDirectoryError(null);
                              }}
                              onKeyDown={(event) => {
                                const action = directoryCreateKeyAction(
                                  event.key,
                                );
                                if (!action) return;
                                event.preventDefault();
                                event.stopPropagation();
                                if (action === "create") {
                                  void submitDirectoryCreate();
                                } else {
                                  invalidateDirectoryCreate(true);
                                }
                              }}
                              aria-invalid={directoryError ? true : undefined}
                              aria-describedby={
                                directoryError
                                  ? "new-directory-error"
                                  : undefined
                              }
                              autoComplete="off"
                              placeholder="Directory name"
                              disabled={directoryCreating}
                            />
                            <Button
                              type="button"
                              size="sm"
                              onClick={() => void submitDirectoryCreate()}
                              disabled={
                                directoryCreating ||
                                directoryName.trim() === ""
                              }
                            >
                              {directoryCreating ? "Creating..." : "Create"}
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => invalidateDirectoryCreate(true)}
                            >
                              Cancel
                            </Button>
                          </div>
                          {directoryError ? (
                            <p
                              id="new-directory-error"
                              role="alert"
                              className="pl-5.5 text-xs text-destructive"
                            >
                              {directoryError}
                            </p>
                          ) : null}
                        </div>
                      ) : (
                        <button
                          ref={createDirectoryButtonRef}
                          type="button"
                          className="flex h-10 w-full cursor-pointer items-center gap-2 px-3 text-left text-sm text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:underline"
                          onClick={beginDirectoryCreate}
                        >
                          <Plus className="size-3.5" />
                          Create directory
                        </button>
                      )}
                    </div>
                  ) : (
                    <div className="px-3 py-6 text-center text-sm text-muted-foreground">
                      Directory listing unavailable.
                    </div>
                  )}
                </ScrollArea>
              </div>

              <div className="grid gap-4 sm:grid-cols-2 sm:items-end">
                <div className="space-y-1.5">
                  <label htmlFor="agent-name" className="text-xs font-medium">
                    Name
                  </label>
                  <Input
                    id="agent-name"
                    className="focus-visible:ring-0 focus-visible:ring-offset-0"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    placeholder="Optional display name"
                  />
                </div>
                <div className="grid grid-cols-2 items-end gap-3">
                  <ToggleField
                    label="Autostart"
                    checked={autostart}
                    onCheckedChange={setAutostart}
                  />
                  <ToggleField
                    label="Start now"
                    checked={startNow}
                    onCheckedChange={setStartNow}
                  />
                </div>
              </div>

              {error ? (
                <div
                  role="alert"
                  className="rounded-md border border-destructive/35 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                >
                  {error}
                </div>
              ) : null}
            </div>
          </div>

          <DialogFooter className="shrink-0">
            <Button
              type="button"
              variant="outline"
              onClick={() => handleOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || !workspace}>
              <Plus className="size-3.5" />
              {submitting ? "Adding..." : "Add agent"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RemoveAgentDialog({
  agent,
  onOpenChange,
  onRemoved,
  onPartialFailure,
}: {
  agent: AgentStatus | null;
  onOpenChange: (open: boolean) => void;
  onRemoved: (agent: AgentStatus) => void;
  onPartialFailure: () => Promise<void>;
}) {
  const [confirmation, setConfirmation] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setConfirmation("");
    setSubmitting(false);
    setError(null);
  }, [agent]);

  if (!agent) return null;
  const confirmationTarget = agent.name || agent.id;
  const confirmed = confirmation === confirmationTarget;
  const selectedAgent = agent;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!confirmed) return;
    setSubmitting(true);
    setError(null);
    try {
      await removeAgent(selectedAgent.id, confirmation);
      onRemoved(selectedAgent);
    } catch (cause) {
      await onPartialFailure();
      setError(
        cause instanceof Error ? cause.message : "Failed to remove agent.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Remove {agent.name || agent.id}</DialogTitle>
          <DialogDescription>
            This permanently deletes the agent's sessions, memory, logs, and
            registry state. The workspace files remain.
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={(event) => void submit(event)}>
          <div className="space-y-1.5">
            <label htmlFor="remove-agent-confirmation" className="text-xs font-medium">
              Type <span className="font-mono">{confirmationTarget}</span> to confirm
            </label>
            <Input
              id="remove-agent-confirmation"
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
            />
          </div>
          {error ? (
            <div
              role="alert"
              className="rounded-md border border-destructive/35 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {error}
            </div>
          ) : null}
          <DialogFooter className="mx-0 mb-0 px-0 pb-0">
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="destructive"
              disabled={!confirmed || submitting}
            >
              <Trash2 className="size-3.5" />
              {submitting ? "Removing..." : "Remove agent"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ToggleField({
  label,
  checked,
  onCheckedChange,
}: {
  label: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex h-8 items-center justify-between gap-2 rounded-md border px-2.5 text-xs">
      {label}
      <Switch checked={checked} onCheckedChange={onCheckedChange} />
    </label>
  );
}

function pathBreadcrumbs(path: string): Array<{ label: string; path: string }> {
  const windows = /^[A-Za-z]:[\\/]/.test(path);
  const normalized = path.replaceAll("\\", "/");
  const segments = normalized.split("/").filter(Boolean);
  if (windows) {
    const drive = segments.shift() || normalized.slice(0, 2);
    const crumbs = [{ label: drive, path: `${drive}\\` }];
    let current = `${drive}\\`;
    for (const segment of segments) {
      current = `${current}${current.endsWith("\\") ? "" : "\\"}${segment}`;
      crumbs.push({ label: segment, path: current });
    }
    return crumbs;
  }
  const crumbs = [{ label: "/", path: "/" }];
  let current = "";
  for (const segment of segments) {
    current += `/${segment}`;
    crumbs.push({ label: segment, path: current });
  }
  return crumbs;
}

function AgentAction({
  label,
  disabled,
  onClick,
  icon,
  destructive = false,
}: {
  label: string;
  disabled: boolean;
  onClick: () => void;
  icon: React.ReactNode;
  destructive?: boolean;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className={cn(
            destructive &&
              "text-destructive hover:bg-destructive/10 hover:text-destructive focus-visible:bg-destructive/10 focus-visible:text-destructive",
          )}
          disabled={disabled}
          onClick={onClick}
          aria-label={label}
        >
          {icon}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function AgentLink({
  label,
  to,
  icon,
}: {
  label: string;
  to: string;
  icon: React.ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button asChild variant="ghost" size="icon-sm">
          <Link to={to} aria-label={label}>
            {icon}
          </Link>
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
