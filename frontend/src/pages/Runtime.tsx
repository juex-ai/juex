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
            <h1 className="text-lg font-semibold">MCP</h1>
            <Badge variant="secondary" className="font-mono text-xs">
              {data.mcp.connected}/{data.mcp.configured} connected
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
                </tr>
              </thead>
              <tbody>
                {data.mcp.servers.length === 0 ? (
                  <tr>
                    <td className="text-muted-foreground px-3 py-3" colSpan={4}>
                      No MCP servers configured.
                    </td>
                  </tr>
                ) : (
                  data.mcp.servers.map((server) => (
                    <tr key={server.name} className="border-t">
                      <td className="px-3 py-2 font-medium">{server.name}</td>
                      <td className="px-3 py-2">
                        <Badge variant={server.connected ? "secondary" : "outline"}>
                          {server.connected ? "connected" : "not connected"}
                        </Badge>
                      </td>
                      <td className="px-3 py-2 font-mono text-xs">
                        {server.tool_count}
                      </td>
                      <td className="max-w-[28rem] truncate px-3 py-2 font-mono text-xs">
                        {[server.command, ...(server.args ?? [])].join(" ")}
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
