import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { getRuntimeStatus } from "@/api";
import type { RuntimeStatusResponse, SystemPromptEntry } from "@/types";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import { formatRuntimeTokenCount } from "@/lib/runtime-display";

export function Runtime() {
  const [data, setData] = useState<RuntimeStatusResponse | null>(null);
  useShellTitle("Runtime");

  useEffect(() => {
    let live = true;
    getRuntimeStatus()
      .then((status) => {
        if (live) setData(status);
      })
      .catch((e) => console.error("getRuntimeStatus failed", e));
    return () => {
      live = false;
    };
  }, []);

  if (!data) {
    return <LoadingState label="Loading runtime" />;
  }

  const systemPrompt = data.system_prompt ?? { count: 0, items: [] };
  const systemPromptItems = systemPrompt.items ?? [];

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
            <Badge variant="secondary" className="font-mono text-[11px]">
              {data.provider.protocol || data.provider.id || "-"}
            </Badge>
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
              MCP
            </h1>
            <Badge
              variant={data.mcp.errors > 0 ? "destructive" : "secondary"}
              className="font-mono text-[11px]"
            >
              {mcpSummaryLabel(data)}
            </Badge>
          </div>
          <div className="overflow-x-auto rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            <table className="w-full min-w-[56rem] text-left text-sm">
              <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Server</th>
                  <th className="px-3 py-2 font-medium">Source</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium">Tools</th>
                  <th className="px-3 py-2 font-medium">Command</th>
                  <th className="px-3 py-2 font-medium">Error</th>
                </tr>
              </thead>
              <tbody>
                {data.mcp.servers.length === 0 ? (
                  <tr>
                    <td className="text-muted-foreground px-3 py-3" colSpan={6}>
                      No MCP servers configured.
                    </td>
                  </tr>
                ) : (
                  data.mcp.servers.map((server) => (
                    <tr key={server.name} className="border-t">
                      <td className="px-3 py-2 font-medium">{server.name}</td>
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
                      <td className="px-3 py-2 font-mono text-xs">
                        {server.tool_count}
                      </td>
                      <td className="max-w-[28rem] truncate px-3 py-2 font-mono text-xs">
                        {[server.command, ...(server.args ?? [])].join(" ")}
                      </td>
                      <td className="text-destructive max-w-[28rem] break-words px-3 py-2 font-mono text-xs">
                        {server.error || "-"}
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
      </div>
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
