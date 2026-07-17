import { useEffect, useState } from "react";
import { RuntimeDisclosureButton } from "@/components/RuntimeDisclosureButton";
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
  formatRuntimeTimestamp,
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
                Started
              </dt>
              <dd className="border-t px-3 py-2 font-mono text-xs">
                {formatRuntimeTimestamp(data.start_time)}
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
          <div className="overflow-x-auto rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            {systemPromptItems.length === 0 ? (
              <div className="text-muted-foreground px-3 py-3 text-sm">
                No system prompt entries.
              </div>
            ) : (
              <SystemPromptTable entries={systemPromptItems} />
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
            <div className="overflow-x-auto rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
              <MCPServerTable servers={mcpServers} />
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

function SystemPromptTable({ entries }: { entries: SystemPromptEntry[] }) {
  return (
    <table className="w-full min-w-[52rem] text-left text-sm">
      <caption className="sr-only">System prompt entries</caption>
      <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
        <tr>
          <th scope="col" className="w-10 px-2 py-2 font-medium">
            <span className="sr-only">Toggle</span>
          </th>
          <th scope="col" className="px-3 py-2 font-medium">
            Label
          </th>
          <th scope="col" className="w-32 px-3 py-2 font-medium">
            Source
          </th>
          <th scope="col" className="px-3 py-2 font-medium">
            Path
          </th>
          <th scope="col" className="w-32 px-3 py-2 font-medium">
            Tokens
          </th>
        </tr>
      </thead>
      <tbody>
        {entries.map((entry, index) => (
          <SystemPromptEntryRow
            key={`${entry.key}:${entry.path || index}`}
            entry={entry}
          />
        ))}
      </tbody>
    </table>
  );
}

function SystemPromptEntryRow({ entry }: { entry: SystemPromptEntry }) {
  const [entryOpen, setEntryOpen] = useState(false);

  return (
    <>
      <tr className="border-t align-top first:border-t-0">
        <td className="px-2 py-2">
          <RuntimeDisclosureButton
            label={`${entry.label} system prompt`}
            open={entryOpen}
            onToggle={() => setEntryOpen((open) => !open)}
          />
        </td>
        <th scope="row" className="px-3 py-2.5 font-medium">
          {entry.label}
        </th>
        <td className="px-3 py-2">
          <Badge variant="outline" className="font-mono text-[11px]">
            {entry.source}
          </Badge>
        </td>
        <td className="px-3 py-2.5">
          <div className="text-muted-foreground max-w-[30rem] truncate font-mono text-xs">
            {entry.path || entry.key}
          </div>
        </td>
        <td className="px-3 py-2">
          <Badge variant="secondary" className="font-mono text-[11px]">
            ~{formatRuntimeTokenCount(entry.tokens)} tokens
          </Badge>
        </td>
      </tr>
      {entryOpen && (
        <tr className="border-t bg-muted/20">
          <td colSpan={5} className="px-3 py-3">
            <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border bg-background p-3 font-mono text-xs leading-relaxed">
              {entry.text}
            </pre>
          </td>
        </tr>
      )}
    </>
  );
}

function MCPServerTable({ servers }: { servers: MCPServerInfo[] }) {
  return (
    <table className="w-full min-w-[72rem] text-left text-sm">
      <caption className="sr-only">MCP servers</caption>
      <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
        <tr>
          <th scope="col" className="w-10 px-2 py-2 font-medium">
            <span className="sr-only">Toggle</span>
          </th>
          <th scope="col" className="px-3 py-2 font-medium">
            Name
          </th>
          <th scope="col" className="w-28 px-3 py-2 font-medium">
            Source
          </th>
          <th scope="col" className="w-32 px-3 py-2 font-medium">
            Status
          </th>
          <th scope="col" className="w-24 px-3 py-2 font-medium">
            Tools
          </th>
          <th scope="col" className="px-3 py-2 font-medium">
            Command
          </th>
          <th scope="col" className="px-3 py-2 font-medium">
            Error
          </th>
        </tr>
      </thead>
      <tbody>
        {servers.map((server) => (
          <MCPServerRow key={server.name} server={server} />
        ))}
      </tbody>
    </table>
  );
}

function MCPServerRow({ server }: { server: MCPServerInfo }) {
  const [serverOpen, setServerOpen] = useState(false);
  const toolsAvailable = server.tools !== undefined;
  const toolCount = server.tool_count ?? server.tools?.length ?? 0;

  return (
    <>
      <tr className="border-t align-top first:border-t-0">
        <td className="px-2 py-2">
          <RuntimeDisclosureButton
            label={`${server.name} MCP tools`}
            open={serverOpen}
            onToggle={() => setServerOpen((open) => !open)}
          />
        </td>
        <th scope="row" className="px-3 py-2.5 font-medium">
          {server.name}
        </th>
        <td className="px-3 py-2">
          <Badge variant="outline" className="font-mono text-[11px]">
            {server.source}
          </Badge>
        </td>
        <td className="px-3 py-2">
          <Badge
            variant={mcpStatusVariant(server.status)}
            className="font-mono text-[11px]"
          >
            {mcpStatusLabel(server.status)}
          </Badge>
        </td>
        <td className="px-3 py-2">
          <Badge variant="secondary" className="font-mono text-[11px]">
            {toolCount}
          </Badge>
        </td>
        <td className="px-3 py-2.5">
          <div className="max-w-[24rem] truncate font-mono text-xs">
            {mcpServerCommand(server.command, server.args)}
          </div>
        </td>
        <td
          className="px-3 py-2.5"
          title={server.error || undefined}
        >
          <div
            className={
              server.error
                ? "max-w-[24rem] truncate font-mono text-xs text-destructive"
                : "max-w-[24rem] truncate font-mono text-xs text-muted-foreground"
            }
          >
            {server.error || "-"}
          </div>
        </td>
      </tr>
      {serverOpen && (
        <tr className="border-t bg-muted/20">
          <td colSpan={7} className="p-0 pl-4">
            <RuntimeToolList
              tools={server.tools ?? []}
              empty={mcpToolEmptyLabel(server.status, toolsAvailable)}
            />
          </td>
        </tr>
      )}
    </>
  );
}

function mcpStatusLabel(status: string): string {
  if (status === "connected") return "connected";
  if (status === "error") return "error";
  return "not started";
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
