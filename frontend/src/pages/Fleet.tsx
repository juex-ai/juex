import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowRight,
  Check,
  ChevronRight,
  FileCog,
  Folder,
  FolderOpen,
  Play,
  Plus,
  Power,
  RefreshCw,
  RotateCw,
  ScrollText,
  Square,
  Trash2,
} from "lucide-react";

import {
  addAgent,
  listAgents,
  listDirectories,
  removeAgent,
  runAgentAction,
  setAgentEnabled,
} from "@/api";
import { LogoMark } from "@/components/LogoMark";
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
import { agentPagePath } from "@/lib/fleet-routes";
import { cn } from "@/lib/utils";
import type {
  AgentRuntimeHealth,
  AgentStatus,
  DirectoryListing,
} from "@/types";

type LifecycleAction = "start" | "stop" | "restart";

export function Fleet() {
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

  async function runAction(agent: AgentStatus, action: LifecycleAction) {
    setBusyAgent(agent.id);
    setError(null);
    try {
      const next = await runAgentAction(agent.id, action);
      replaceAgent(next);
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
    <div className="flex h-svh min-h-0 flex-col overflow-hidden bg-background">
      <header className="flex h-[var(--juex-header-height)] shrink-0 items-center gap-3 border-b bg-card px-4 shadow-[var(--shadow-xs)]">
        <div className="flex items-center gap-2 text-primary">
          <LogoMark className="size-6" />
          <span className="font-serif text-2xl italic leading-tight">juex</span>
        </div>
        <span className="h-5 border-l" aria-hidden="true" />
        <span className="text-sm font-medium text-foreground">Fleet</span>
        <div className="ml-auto flex items-center gap-1">
          <Button type="button" size="sm" onClick={() => setAddOpen(true)}>
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
            <RefreshCw className={cn("size-4", refreshing && "animate-spin")} />
          </Button>
        </div>
      </header>

      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-7xl flex-col gap-4 px-4 py-6 md:px-6">
          <div>
            <h1 className="text-xl font-semibold text-foreground">Agents</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              Registered workspaces and their current runtime state.
            </p>
          </div>

          {error ? (
            <div
              role="alert"
              className="rounded-md border border-destructive/35 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {error}
            </div>
          ) : null}

          <div className="overflow-x-auto rounded-md border bg-card shadow-[var(--shadow-xs)]">
            <div className="min-w-[64rem]">
              <div className="grid grid-cols-[minmax(13rem,1fr)_8rem_minmax(18rem,1.4fr)_9rem_8rem_16rem] bg-muted/60 text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
                <div className="px-3 py-2 font-medium">Agent</div>
                <div className="px-3 py-2 font-medium">Health</div>
                <div className="px-3 py-2 font-medium">Workspace</div>
                <div className="px-3 py-2 font-medium">Process</div>
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
        onOpenChange={setAddOpen}
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
  return (
    <div className="grid grid-cols-[minmax(13rem,1fr)_8rem_minmax(18rem,1.4fr)_9rem_8rem_16rem] items-center text-sm">
      <div className="min-w-0 px-3 py-3">
        <Link
          to={base}
          className="block truncate font-medium text-foreground outline-none hover:text-primary focus-visible:ring-2 focus-visible:ring-ring/35"
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
      <div className="px-3 py-3">
        <HealthBadge health={agent.runtime_health} />
        <div className="mt-1 font-mono text-[10px] text-muted-foreground">
          {agent.binding}
        </div>
      </div>
      <div
        className="truncate px-3 py-3 font-mono text-xs text-muted-foreground"
        title={agent.workspace || "Workspace unavailable"}
      >
        {agent.workspace || "-"}
      </div>
      <div className="px-3 py-3 font-mono text-xs text-muted-foreground">
        {agent.pid ? `pid ${agent.pid}` : "-"}
      </div>
      <div className="px-3 py-3">
        <Badge
          variant="outline"
          className={cn(
            "text-[10px]",
            agent.enabled
              ? "border-emerald-600/35 text-emerald-700"
              : "border-border text-muted-foreground",
          )}
        >
          {agent.enabled ? "enabled" : "disabled"}
        </Badge>
      </div>
      <div className="flex items-center justify-end gap-1 px-3 py-3">
        <TooltipProvider delayDuration={250}>
          <AgentAction
            label="Start agent"
            disabled={
              busy || !agent.enabled || agent.runtime_health === "healthy"
            }
            onClick={() => onAction("start")}
            icon={<Play className="size-3.5" />}
          />
          <AgentAction
            label="Stop agent"
            disabled={busy || agent.runtime_health === "stopped"}
            onClick={() => onAction("stop")}
            icon={<Square className="size-3.5" />}
          />
          <AgentAction
            label="Restart agent"
            disabled={
              busy || !agent.enabled || agent.runtime_health === "stopped"
            }
            onClick={() => onAction("restart")}
            icon={<RotateCw className={cn("size-3.5", busy && "animate-spin")} />}
          />
          <AgentAction
            label={agent.enabled ? "Disable agent" : "Enable agent"}
            disabled={busy}
            onClick={onToggleEnabled}
            icon={<Power className="size-3.5" />}
          />
          <AgentLink label="View logs" to={`${base}/logs`} icon={<ScrollText className="size-3.5" />} />
          <AgentLink label="Edit config" to={`${base}/config`} icon={<FileCog className="size-3.5" />} />
          <AgentLink label="Open agent" to={base} icon={<ArrowRight className="size-3.5" />} />
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
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const browse = useCallback(
    async (path: string | undefined, hidden: boolean) => {
      setBrowsing(true);
      setError(null);
      try {
        const next = await listDirectories(path, hidden);
        setListing(next);
        setWorkspace(next.path);
      } catch (cause) {
        setError(
          cause instanceof Error ? cause.message : "Failed to browse directories.",
        );
      } finally {
        setBrowsing(false);
      }
    },
    [],
  );

  useEffect(() => {
    if (!open) return;
    setWorkspace("");
    setName("");
    setAutostart(false);
    setStartNow(false);
    setShowHidden(false);
    setListing(null);
    setError(null);
    void browse(undefined, false);
  }, [open, browse]);

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
      onOpenChange(false);
    } catch (cause) {
      await onPartialFailure();
      setError(cause instanceof Error ? cause.message : "Failed to add agent.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[calc(100svh-2rem)] flex-col overflow-hidden sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add agent</DialogTitle>
          <DialogDescription>
            Register an existing workspace from this machine.
          </DialogDescription>
        </DialogHeader>
        <form
          className="min-h-0 flex-1 space-y-4 overflow-y-auto pr-1"
          onSubmit={(event) => void submit(event)}
        >
          <div className="space-y-1.5">
            <label htmlFor="agent-workspace" className="text-xs font-medium">
              Workspace path
            </label>
            <div className="flex gap-2">
              <Input
                id="agent-workspace"
                className="font-mono text-xs"
                value={workspace}
                onChange={(event) => setWorkspace(event.target.value)}
                placeholder="/absolute/path/to/workspace"
                required
              />
              <Button
                type="button"
                variant="outline"
                onClick={() => void browse(workspace, showHidden)}
                disabled={browsing || !workspace}
              >
                <FolderOpen className="size-3.5" />
                Browse
              </Button>
            </div>
          </div>

          <div className="overflow-hidden rounded-md border">
            <div className="flex min-h-9 items-center gap-1 border-b bg-muted/45 px-2">
              <div className="flex min-w-0 flex-1 items-center overflow-x-auto">
                {listing
                  ? pathBreadcrumbs(listing.path).map((crumb, index) => (
                      <div key={crumb.path} className="flex shrink-0 items-center">
                        {index > 0 ? (
                          <ChevronRight className="size-3 text-muted-foreground" />
                        ) : null}
                        <Button
                          type="button"
                          variant="ghost"
                          size="xs"
                          className="font-mono"
                          onClick={() => void browse(crumb.path, showHidden)}
                        >
                          {crumb.label}
                        </Button>
                      </div>
                    ))
                  : null}
              </div>
              <label className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
                Show hidden
                <Switch
                  checked={showHidden}
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
              ) : listing?.dirs.length ? (
                <div className="divide-y">
                  {listing.dirs.map((directory) => (
                    <div
                      key={directory.path}
                      className="flex h-10 items-center gap-2 px-2"
                    >
                      <Button
                        type="button"
                        variant="ghost"
                        className="min-w-0 flex-1 justify-start"
                        onClick={() => void browse(directory.path, showHidden)}
                        title={directory.path}
                      >
                        <Folder className="size-3.5 text-muted-foreground" />
                        <span className="truncate">{directory.name}</span>
                        {directory.registered ? (
                          <Badge variant="outline" className="ml-auto text-[10px]">
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
                        onClick={() => setWorkspace(directory.path)}
                      >
                        <Check className="size-3.5" />
                      </Button>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="px-3 py-6 text-center text-sm text-muted-foreground">
                  No subdirectories.
                </div>
              )}
            </ScrollArea>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label htmlFor="agent-name" className="text-xs font-medium">
                Name
              </label>
              <Input
                id="agent-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="Optional display name"
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
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

          <DialogFooter className="mx-0 mb-0 px-0 pb-0">
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
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
          variant={destructive ? "destructive" : "ghost"}
          size="icon-sm"
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

function HealthBadge({ health }: { health: AgentRuntimeHealth }) {
  return (
    <Badge
      variant="outline"
      className={cn(
        "font-mono text-[10px]",
        health === "healthy" && "border-emerald-600/35 text-emerald-700",
        health === "unhealthy" && "border-destructive/35 text-destructive",
        health === "ambiguous" && "border-amber-600/35 text-amber-700",
      )}
    >
      {health}
    </Badge>
  );
}
