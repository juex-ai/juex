import { useEffect, useState } from "react";
import { ChevronRightIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  RuntimeToolGroups,
  RuntimeToolList,
} from "@/components/RuntimeToolCatalog";
import { getRuntimeStatus } from "@/api";
import type {
  MCPServerInfo,
  RuntimeHookInfo,
  RuntimeStatusResponse,
  SystemPromptEntry,
} from "@/types";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import {
  formatRuntimeTokenCount,
  runtimeHookCommandLabel,
  runtimeHooksSummaryLabel,
} from "@/lib/runtime-display";

export function Runtime() {
  const [data, setData] = useState<RuntimeStatusResponse | null>(null);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [error, setError] = useState<string | null>(null);
  useShellTitle("Runtime");

  useEffect(() => {
    let live = true;
    let timer: number | undefined;
    const load = () => {
      getRuntimeStatus()
        .then((status) => {
          if (!live) return;
          setData(status);
          setLastUpdated(new Date());
          setError(null);
        })
        .catch((e) => {
          console.error("getRuntimeStatus failed", e);
          if (live) setError(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          if (live) timer = window.setTimeout(load, 3000);
        });
    };
    load();
    return () => {
      live = false;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, []);

  if (!data) {
    return <LoadingState label="Loading runtime" />;
  }

  const systemPrompt = data.system_prompt ?? { count: 0, items: [] };
  const systemPromptItems = systemPrompt.items ?? [];
  const hooks = data.hooks ?? { configured: 0, commands: [] };
  const hookCommands = hooks.commands ?? [];
  const runtimeTools = data.tools ?? { count: 0, groups: [] };
  const runtimeToolGroups = runtimeTools.groups ?? [];
  const mcpServers = data.mcp?.servers ?? [];

  return (
    <div className="min-h-0 flex-1 overflow-auto bg-background">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-8 px-4 py-5 md:px-6 md:py-6">
        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              Runtime
            </h1>
            <Badge variant="secondary" className="font-mono text-[11px]">
              service
            </Badge>
          </div>
          <div className="overflow-hidden rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            <dl className="grid gap-0 text-sm sm:grid-cols-[9rem_minmax(0,1fr)]">
              <dt className="border-b bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground sm:border-b-0">
                CWD
              </dt>
              <dd className="break-all px-3 py-2 font-mono text-xs">
                {data.work_dir || "-"}
              </dd>
              <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                Shell
              </dt>
              <dd className="border-t px-3 py-2">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <Badge variant="outline" className="font-mono text-[11px]">
                    {shellLabel(data)}
                  </Badge>
                  <span className="min-w-0 break-all font-mono text-xs">
                    {shellCommand(data)}
                  </span>
                </div>
              </dd>
              <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                Sandbox
              </dt>
              <dd className="border-t px-3 py-2">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <Badge
                    variant={data.sandbox?.enabled ? "secondary" : "outline"}
                    className="font-mono text-[11px]"
                  >
                    {data.sandbox?.enabled ? "enabled" : "disabled"}
                  </Badge>
                  <Badge variant="outline" className="font-mono text-[11px]">
                    outside {data.sandbox?.file_system?.outside_workspace || "-"}
                  </Badge>
                  <Badge variant="outline" className="font-mono text-[11px]">
                    network {data.sandbox?.network?.enabled ? "on" : "off"}
                  </Badge>
                </div>
              </dd>
              <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                Last refresh
              </dt>
              <dd className="border-t px-3 py-2 font-mono text-xs">
                {lastUpdated ? lastUpdated.toLocaleTimeString() : "-"}
              </dd>
              {error ? (
                <>
                  <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Refresh error
                  </dt>
                  <dd className="text-destructive border-t break-words px-3 py-2 font-mono text-xs">
                    {error}
                  </dd>
                </>
              ) : null}
            </dl>
          </div>
        </section>

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              System Prompt
            </h1>
            <Badge variant="secondary" className="font-mono text-[11px]">
              {systemPrompt.count}
            </Badge>
          </div>
          <div className="overflow-hidden rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            {systemPromptItems.length === 0 ? (
              <div className="text-muted-foreground px-3 py-3 text-sm">
                No system prompt entries.
              </div>
            ) : (
              systemPromptItems.map((entry, index) => (
                <SystemPromptEntryRow
                  key={`${entry.key}:${entry.path || index}`}
                  entry={entry}
                />
              ))
            )}
          </div>
        </section>

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              Provider
            </h1>
          </div>
          <div className="overflow-x-auto rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            <table className="w-full min-w-[34rem] text-left text-sm">
              <tbody>
                <tr>
                  <th className="w-36 bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    ID
                  </th>
                  <td className="px-3 py-2 font-mono text-xs">
                    {data.provider.id || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Protocol
                  </th>
                  <td className="px-3 py-2 font-mono text-xs">
                    {data.provider.protocol || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Model
                  </th>
                  <td className="px-3 py-2 font-mono text-xs">
                    {data.provider.model || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Base URL
                  </th>
                  <td className="max-w-[42rem] truncate px-3 py-2 font-mono text-xs">
                    {data.provider.base_url || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Capabilities
                  </th>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-2">
                      {providerCapabilities(data).map((cap) => (
                        <Badge key={cap} variant="outline" className="font-mono text-[11px]">
                          {cap}
                        </Badge>
                      ))}
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              Tools
            </h1>
            <Badge variant="secondary" className="font-mono text-[11px]">
              {runtimeTools.count ?? 0}
            </Badge>
          </div>
          <div className="overflow-hidden rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            <RuntimeToolGroups groups={runtimeToolGroups} />
          </div>
        </section>

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              MCP
            </h1>
            <Badge
              variant={data.mcp.errors > 0 ? "destructive" : "secondary"}
              className="font-mono text-[11px]"
            >
              {mcpSummaryLabel(data)}
            </Badge>
          </div>
          {mcpServers.length === 0 ? (
            <div className="text-muted-foreground rounded-[14px] border bg-card px-3 py-3 text-sm shadow-[var(--shadow-sm)]">
              No MCP servers configured.
            </div>
          ) : (
            <div className="space-y-2">
              {mcpServers.map((server) => (
                <MCPServerCard key={server.name} server={server} />
              ))}
            </div>
          )}
        </section>

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              Skills
            </h1>
            <Badge variant="secondary" className="font-mono text-[11px]">
              {data.skills.count}
            </Badge>
          </div>
          <div className="overflow-x-auto rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            <table className="w-full min-w-[56rem] text-left text-sm">
              <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Name</th>
                  <th className="px-3 py-2 font-medium">Source</th>
                  <th className="px-3 py-2 font-medium">Description</th>
                  <th className="px-3 py-2 font-medium">Path</th>
                </tr>
              </thead>
              <tbody>
                {data.skills.items.length === 0 ? (
                  <tr>
                    <td className="text-muted-foreground px-3 py-3" colSpan={4}>
                      No skills loaded.
                    </td>
                  </tr>
                ) : (
                  data.skills.items.map((skill) => (
                    <tr key={skill.name} className="border-t">
                      <td className="px-3 py-2 font-medium">{skill.name}</td>
                      <td className="px-3 py-2">
                        <Badge variant="outline" className="font-mono text-[11px]">
                          {skill.source}
                        </Badge>
                      </td>
                      <td className="max-w-[24rem] px-3 py-2">
                        <span className="line-clamp-2">
                          {skill.description || "-"}
                        </span>
                      </td>
                      <td className="max-w-[24rem] truncate px-3 py-2 font-mono text-xs">
                        {skill.path}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </section>

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              Hooks
            </h1>
            <Badge variant="secondary" className="font-mono text-[11px]">
              {runtimeHooksSummaryLabel(hooks)}
            </Badge>
          </div>
          <div className="overflow-x-auto rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            <table className="w-full min-w-[56rem] text-left text-sm">
              <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Name</th>
                  <th className="px-3 py-2 font-medium">Source</th>
                  <th className="px-3 py-2 font-medium">Events</th>
                  <th className="px-3 py-2 font-medium">Tools</th>
                  <th className="px-3 py-2 font-medium">Command</th>
                  <th className="px-3 py-2 font-medium">Limits</th>
                </tr>
              </thead>
              <tbody>
                {hookCommands.length === 0 ? (
                  <tr>
                    <td className="text-muted-foreground px-3 py-3" colSpan={6}>
                      No hooks configured.
                    </td>
                  </tr>
                ) : (
                  hookCommands.map((hook, index) => (
                    <HookRow hook={hook} key={`${hook.name}:${index}`} />
                  ))
                )}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  );
}

function HookRow({ hook }: { hook: RuntimeHookInfo }) {
  return (
    <tr className="border-t">
      <td className="px-3 py-2 font-medium">{hook.name || "-"}</td>
      <td className="px-3 py-2">
        {hook.source ? (
          <Badge variant="outline" className="font-mono text-[11px]">
            {hook.source}
          </Badge>
        ) : (
          <span className="text-muted-foreground">-</span>
        )}
      </td>
      <td className="px-3 py-2">
        <BadgeList items={hook.events} />
      </td>
      <td className="px-3 py-2">
        <BadgeList items={hook.tools ?? []} empty="all" />
      </td>
      <td className="max-w-[28rem] truncate px-3 py-2 font-mono text-xs">
        {runtimeHookCommandLabel(hook.command)}
      </td>
      <td className="whitespace-nowrap px-3 py-2 font-mono text-xs">
        {hook.timeout_seconds}s / {hook.max_output_bytes} bytes
      </td>
    </tr>
  );
}

function BadgeList({ items, empty = "-" }: { items: string[]; empty?: string }) {
  if (items.length === 0) {
    return <span className="text-muted-foreground">{empty}</span>;
  }
  return (
    <div className="flex min-w-0 flex-wrap gap-1.5">
      {items.map((item) => (
        <Badge key={item} variant="outline" className="font-mono text-[11px]">
          {item}
        </Badge>
      ))}
    </div>
  );
}

function SystemPromptEntryRow({ entry }: { entry: SystemPromptEntry }) {
  return (
    <details className="group border-t first:border-t-0">
      <summary className="flex cursor-pointer list-none flex-wrap items-center justify-between gap-3 px-3 py-3 marker:hidden">
        <div className="min-w-0 space-y-1">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="font-medium">{entry.label}</span>
            <Badge variant="outline" className="font-mono text-[11px]">
              {entry.source}
            </Badge>
          </div>
          <div className="text-muted-foreground break-all font-mono text-xs">
            {entry.path || entry.key}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Badge variant="secondary" className="font-mono text-[11px]">
            ~{formatRuntimeTokenCount(entry.tokens)} tokens
          </Badge>
          <span
            aria-hidden="true"
            className="text-muted-foreground text-xs transition-transform group-open:rotate-90"
          >
            &gt;
          </span>
        </div>
      </summary>
      <div className="border-t bg-muted/30 px-3 py-3">
        <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border bg-background p-3 font-mono text-xs leading-relaxed">
          {entry.text}
        </pre>
      </div>
    </details>
  );
}

function mcpStatusLabel(status: string): string {
  if (status === "connected") return "connected";
  if (status === "error") return "error";
  return "not started";
}

function MCPServerCard({ server }: { server: MCPServerInfo }) {
  const [serverOpen, setServerOpen] = useState(false);
  const toolsAvailable = server.tools !== undefined;

  return (
    <div className="overflow-hidden rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
      <div className="px-3 py-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="font-medium">{server.name}</span>
          <Badge variant="outline" className="font-mono text-[11px]">
            {server.source}
          </Badge>
          <Badge
            variant={mcpStatusVariant(server.status)}
            className="font-mono text-[11px]"
          >
            {mcpStatusLabel(server.status)}
          </Badge>
          <Badge variant="secondary" className="font-mono text-[11px]">
            {server.tool_count ?? server.tools?.length ?? 0} tools
          </Badge>
        </div>
        <dl className="mt-2 grid min-w-0 gap-x-3 gap-y-1 text-xs sm:grid-cols-[5rem_minmax(0,1fr)]">
          <dt className="text-muted-foreground font-medium">Command</dt>
          <dd className="min-w-0 break-all font-mono">
            {mcpServerCommand(server.command, server.args)}
          </dd>
          <dt className="text-muted-foreground font-medium">Error</dt>
          <dd
            className={
              server.error
                ? "min-w-0 break-words font-mono text-destructive"
                : "min-w-0 font-mono text-muted-foreground"
            }
          >
            {server.error || "-"}
          </dd>
        </dl>
      </div>
      <details
        className="group/runtime-mcp-server border-t"
        onToggle={(event) => setServerOpen(event.currentTarget.open)}
      >
        <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 outline-none marker:hidden hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35">
          <ChevronRightIcon
            aria-hidden="true"
            className="text-muted-foreground size-4 shrink-0 transition-transform group-open/runtime-mcp-server:rotate-90"
          />
          <span className="font-medium">Tool details</span>
          <span className="text-muted-foreground min-w-0 flex-1 truncate font-mono text-xs">
            {server.tools?.map((tool) => tool.name).join(", ") || "No tool preview"}
          </span>
        </summary>
        {serverOpen && (
          <div className="border-t bg-muted/20 pl-4">
            <RuntimeToolList
              tools={server.tools ?? []}
              empty={mcpToolEmptyLabel(server.status, toolsAvailable)}
            />
          </div>
        )}
      </details>
    </div>
  );
}

function mcpServerCommand(command: string, args?: string[]): string {
  return [command, ...(args ?? [])].filter(Boolean).join(" ") || "-";
}

function mcpToolEmptyLabel(status: string, toolsAvailable: boolean): string {
  if (!toolsAvailable) {
    return "Tool details unavailable in this response";
  }
  if (status === "error") {
    return "No tools available because this server failed to start.";
  }
  if (status !== "connected") {
    return "No tools available because this server has not started.";
  }
  return "This server advertised no tools.";
}

function providerCapabilities(data: RuntimeStatusResponse): string[] {
  const caps = data.provider.capabilities;
  const labels = [
    caps.tools ? "tools" : "",
    caps.streaming ? "streaming" : "",
    caps.reasoning_effort ? "reasoning effort" : "",
    caps.reasoning_replay ? "reasoning replay" : "",
    caps.max_output_tokens ? "max output" : "",
  ].filter(Boolean);
  return labels.length > 0 ? labels : ["none"];
}

function shellLabel(data: RuntimeStatusResponse): string {
  const shell = data.shell;
  const profile = shell?.profile || "-";
  const family = shell?.family || "-";
  const style = shell?.path_style || "-";
  return `${profile} · ${family} · ${style} paths`;
}

function shellCommand(data: RuntimeStatusResponse): string {
  const shell = data.shell;
  if (!shell?.binary) return "-";
  return [shell.binary, ...(shell.args ?? []), "<cmd>"].join(" ");
}

function mcpSummaryLabel(data: RuntimeStatusResponse): string {
  const base = `${data.mcp.connected}/${data.mcp.configured} connected`;
  if (data.mcp.errors === 0) return base;
  return `${base}, ${data.mcp.errors} ${
    data.mcp.errors === 1 ? "error" : "errors"
  }`;
}

function mcpStatusVariant(status: string): "secondary" | "outline" | "destructive" {
  if (status === "connected") return "secondary";
  if (status === "error") return "destructive";
  return "outline";
}
