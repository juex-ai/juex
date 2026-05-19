import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { getRuntimeStatus } from "@/api";
import type { RuntimeStatusResponse } from "@/types";

export function Runtime() {
  const [data, setData] = useState<RuntimeStatusResponse | null>(null);

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
    return <div className="text-muted-foreground p-8">Loading...</div>;
  }

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-8 px-6 py-6">
        <section className="space-y-3">
          <div className="flex items-center justify-between gap-3">
            <h1 className="text-lg font-semibold">Provider</h1>
            <Badge variant="secondary" className="font-mono text-xs">
              {data.provider.protocol || data.provider.type || "-"}
            </Badge>
          </div>
          <div className="overflow-hidden rounded-lg border">
            <table className="w-full text-left text-sm">
              <tbody>
                <tr>
                  <th className="bg-muted/60 text-muted-foreground w-36 px-3 py-2 font-medium">
                    ID
                  </th>
                  <td className="px-3 py-2 font-mono text-xs">
                    {data.provider.id || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 text-muted-foreground px-3 py-2 font-medium">
                    Auth
                  </th>
                  <td className="px-3 py-2 font-mono text-xs">
                    {data.provider.auth || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 text-muted-foreground px-3 py-2 font-medium">
                    Model
                  </th>
                  <td className="px-3 py-2 font-mono text-xs">
                    {data.provider.model || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 text-muted-foreground px-3 py-2 font-medium">
                    Base URL
                  </th>
                  <td className="max-w-[42rem] truncate px-3 py-2 font-mono text-xs">
                    {data.provider.base_url || "-"}
                  </td>
                </tr>
                <tr className="border-t">
                  <th className="bg-muted/60 text-muted-foreground px-3 py-2 font-medium">
                    Capabilities
                  </th>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-2">
                      {providerCapabilities(data).map((cap) => (
                        <Badge key={cap} variant="outline">
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
          <div className="flex items-center justify-between gap-3">
            <h1 className="text-lg font-semibold">MCP</h1>
            <Badge
              variant={data.mcp.errors > 0 ? "destructive" : "secondary"}
              className="font-mono text-xs"
            >
              {mcpSummaryLabel(data)}
            </Badge>
          </div>
          <div className="overflow-hidden rounded-lg border">
            <table className="w-full text-left text-sm">
              <thead className="bg-muted/60 text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Server</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium">Tools</th>
                  <th className="px-3 py-2 font-medium">Command</th>
                  <th className="px-3 py-2 font-medium">Error</th>
                </tr>
              </thead>
              <tbody>
                {data.mcp.servers.length === 0 ? (
                  <tr>
                    <td className="text-muted-foreground px-3 py-3" colSpan={5}>
                      No MCP servers configured.
                    </td>
                  </tr>
                ) : (
                  data.mcp.servers.map((server) => (
                    <tr key={server.name} className="border-t">
                      <td className="px-3 py-2 font-medium">{server.name}</td>
                      <td className="px-3 py-2">
                        <Badge variant={mcpStatusVariant(server.status)}>
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
          <div className="flex items-center justify-between gap-3">
            <h1 className="text-lg font-semibold">Skills</h1>
            <Badge variant="secondary" className="font-mono text-xs">
              {data.skills.count}
            </Badge>
          </div>
          <div className="overflow-hidden rounded-lg border">
            <table className="w-full text-left text-sm">
              <thead className="bg-muted/60 text-muted-foreground">
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
                        <Badge variant="outline">{skill.source}</Badge>
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
