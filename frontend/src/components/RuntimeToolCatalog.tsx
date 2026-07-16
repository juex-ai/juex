import { ChevronRightIcon } from "lucide-react";

import { CodeBlock } from "@/components/ai-elements/code-block";
import { Badge } from "@/components/ui/badge";
import {
  formatRuntimeToolSchema,
  runtimeToolGroupLabel,
  runtimeToolParameters,
  runtimeToolTimeoutLabel,
} from "@/lib/runtime-tool-catalog";
import type { RuntimeToolGroup, RuntimeToolInfo } from "@/types";

interface RuntimeToolListProps {
  tools?: RuntimeToolInfo[];
  empty: string;
}

export function RuntimeToolList({ tools = [], empty }: RuntimeToolListProps) {
  if (tools.length === 0) {
    return <div className="text-muted-foreground px-3 py-3 text-sm">{empty}</div>;
  }

  return (
    <div>
      {tools.map((tool) => (
        <RuntimeToolRow key={tool.name} tool={tool} />
      ))}
    </div>
  );
}

export function RuntimeToolGroups({ groups }: { groups: RuntimeToolGroup[] }) {
  if (groups.length === 0) {
    return (
      <div className="text-muted-foreground px-3 py-3 text-sm">
        No builtin tools registered.
      </div>
    );
  }

  return (
    <div>
      {groups.map((group) => (
        <details
          className="group/runtime-tool-group border-t first:border-t-0"
          key={group.group}
        >
          <summary className="flex cursor-pointer list-none items-center gap-3 px-3 py-3 outline-none marker:hidden hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35">
            <ChevronRightIcon
              aria-hidden="true"
              className="text-muted-foreground size-4 shrink-0 transition-transform group-open/runtime-tool-group:rotate-90"
            />
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <span className="font-medium">
                  {runtimeToolGroupLabel(group.group)}
                </span>
                <Badge variant="secondary" className="font-mono text-[11px]">
                  {group.tools?.length ?? 0}
                </Badge>
              </div>
              <div className="text-muted-foreground mt-1 truncate font-mono text-xs">
                {(group.tools ?? []).map((tool) => tool.name).join(", ") ||
                  "No tools"}
              </div>
            </div>
          </summary>
          <div className="border-t bg-muted/20 pl-4">
            <RuntimeToolList
              tools={group.tools ?? []}
              empty="No tools registered in this group."
            />
          </div>
        </details>
      ))}
    </div>
  );
}

function RuntimeToolRow({ tool }: { tool: RuntimeToolInfo }) {
  const parameters = runtimeToolParameters(tool.schema);
  const timeout = runtimeToolTimeoutLabel(tool.timeout);

  return (
    <details className="group/runtime-tool border-t first:border-t-0">
      <summary className="flex cursor-pointer list-none items-center gap-3 px-3 py-2.5 outline-none marker:hidden hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35">
        <ChevronRightIcon
          aria-hidden="true"
          className="text-muted-foreground size-3.5 shrink-0 transition-transform group-open/runtime-tool:rotate-90"
        />
        <span className="min-w-0 flex-1 truncate font-mono text-xs font-medium">
          {tool.name}
        </span>
        <Badge variant="outline" className="font-mono text-[11px]">
          {timeout}
        </Badge>
      </summary>
      <div className="space-y-4 border-t bg-background px-3 py-3">
        <div className="space-y-1">
          <p className="text-sm leading-relaxed">
            {tool.description || "No description provided."}
          </p>
          <p className="text-muted-foreground font-mono text-xs">
            Timeout: {timeout}
          </p>
        </div>

        <div className="overflow-x-auto rounded-md border">
          {parameters.length === 0 ? (
            <div className="text-muted-foreground px-3 py-3 text-sm">
              No top-level parameters.
            </div>
          ) : (
            <table className="w-full min-w-[38rem] text-left text-sm">
              <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 font-medium">Name</th>
                  <th className="px-3 py-2 font-medium">Type</th>
                  <th className="px-3 py-2 font-medium">Required</th>
                  <th className="px-3 py-2 font-medium">Description</th>
                </tr>
              </thead>
              <tbody>
                {parameters.map((parameter) => (
                  <tr className="border-t align-top" key={parameter.name}>
                    <td className="px-3 py-2 font-mono text-xs font-medium">
                      {parameter.name}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs">
                      {parameter.type}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs">
                      {parameter.required ? "yes" : "no"}
                    </td>
                    <td className="max-w-[30rem] px-3 py-2">
                      {parameter.description || (
                        <span className="text-muted-foreground">-</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <details className="group/runtime-tool-schema rounded-md border">
          <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 font-medium outline-none marker:hidden hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/35">
            <ChevronRightIcon
              aria-hidden="true"
              className="text-muted-foreground size-3.5 transition-transform group-open/runtime-tool-schema:rotate-90"
            />
            <span className="text-sm">Raw JSON schema</span>
          </summary>
          <div className="border-t p-3">
            <CodeBlock
              className="max-h-96 overflow-auto"
              code={formatRuntimeToolSchema(tool.schema)}
              language="json"
            />
          </div>
        </details>
      </div>
    </details>
  );
}
