import { useState } from "react";

import { CodeBlock } from "@/components/ai-elements/code-block";
import { RuntimeDisclosureButton } from "@/components/RuntimeDisclosureButton";
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
    <div className="overflow-x-auto">
      <table className="w-full min-w-[48rem] text-left text-sm">
        <caption className="sr-only">Runtime tools</caption>
        <thead className="bg-muted/50 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
          <tr>
            <th scope="col" className="w-10 px-2 py-2 font-medium">
              <span className="sr-only">Toggle</span>
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Name
            </th>
            <th scope="col" className="w-36 px-3 py-2 font-medium">
              Timeout
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Description
            </th>
          </tr>
        </thead>
        <tbody>
          {tools.map((tool) => (
            <RuntimeToolRow key={tool.name} tool={tool} />
          ))}
        </tbody>
      </table>
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
    <div className="overflow-x-auto">
      <table className="w-full min-w-[44rem] text-left text-sm">
        <caption className="sr-only">Builtin tool groups</caption>
        <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
          <tr>
            <th scope="col" className="w-10 px-2 py-2 font-medium">
              <span className="sr-only">Toggle</span>
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Group
            </th>
            <th scope="col" className="w-24 px-3 py-2 font-medium">
              Count
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Tools
            </th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => (
            <RuntimeToolGroupRow group={group} key={group.group} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RuntimeToolGroupRow({ group }: { group: RuntimeToolGroup }) {
  const [groupOpen, setGroupOpen] = useState(false);
  const label = runtimeToolGroupLabel(group.group);

  return (
    <>
      <tr className="border-t align-top first:border-t-0">
        <td className="px-2 py-2">
          <RuntimeDisclosureButton
            label={`${label} tools`}
            open={groupOpen}
            onToggle={() => setGroupOpen((open) => !open)}
          />
        </td>
        <th scope="row" className="px-3 py-2.5 font-medium">
          {label}
        </th>
        <td className="px-3 py-2">
          <Badge variant="secondary" className="font-mono text-[11px]">
            {group.tools?.length ?? 0}
          </Badge>
        </td>
        <td className="px-3 py-2.5">
          <div className="text-muted-foreground max-w-[34rem] truncate font-mono text-xs">
            {(group.tools ?? []).map((tool) => tool.name).join(", ") ||
              "No tools"}
          </div>
        </td>
      </tr>
      {groupOpen && (
        <tr className="border-t bg-muted/20">
          <td colSpan={4} className="p-0 pl-4">
            <RuntimeToolList
              tools={group.tools ?? []}
              empty="No tools registered in this group."
            />
          </td>
        </tr>
      )}
    </>
  );
}

function RuntimeToolRow({ tool }: { tool: RuntimeToolInfo }) {
  const [toolOpen, setToolOpen] = useState(false);
  const timeout = runtimeToolTimeoutLabel(tool.timeout);

  return (
    <>
      <tr className="border-t align-top first:border-t-0">
        <td className="px-2 py-2">
          <RuntimeDisclosureButton
            label={`${tool.name} details`}
            open={toolOpen}
            onToggle={() => setToolOpen((open) => !open)}
          />
        </td>
        <th
          scope="row"
          className="px-3 py-2.5 font-mono text-xs font-medium"
        >
          {tool.name}
        </th>
        <td className="px-3 py-2">
          <Badge variant="outline" className="font-mono text-[11px]">
            {timeout}
          </Badge>
        </td>
        <td className="max-w-[30rem] px-3 py-2.5">
          <span className="line-clamp-2">
            {tool.description || (
              <span className="text-muted-foreground">No description provided.</span>
            )}
          </span>
        </td>
      </tr>
      {toolOpen && (
        <tr className="border-t">
          <td colSpan={4} className="p-0">
            <RuntimeToolDetails timeout={timeout} tool={tool} />
          </td>
        </tr>
      )}
    </>
  );
}

function RuntimeToolDetails({
  tool,
  timeout,
}: {
  tool: RuntimeToolInfo;
  timeout: string;
}) {
  const parameters = runtimeToolParameters(tool.schema);

  return (
    <div className="space-y-4 bg-background px-3 py-3">
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
            <caption className="sr-only">Parameters for {tool.name}</caption>
            <thead className="bg-muted/60 text-[11px] uppercase tracking-[0.14em] text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  Name
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  Type
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  Required
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  Description
                </th>
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

      <RuntimeToolSchema schema={tool.schema} />
    </div>
  );
}

function RuntimeToolSchema({ schema }: { schema: unknown }) {
  const [schemaOpen, setSchemaOpen] = useState(false);

  return (
    <div className="rounded-md border">
      <div className="flex items-center gap-2 px-2 py-1.5">
        <RuntimeDisclosureButton
          label="raw JSON schema"
          open={schemaOpen}
          onToggle={() => setSchemaOpen((open) => !open)}
        />
        <span className="text-sm">Raw JSON schema</span>
      </div>
      {schemaOpen && <RuntimeToolSchemaBody schema={schema} />}
    </div>
  );
}

function RuntimeToolSchemaBody({ schema }: { schema: unknown }) {
  return (
    <div className="border-t p-3">
      <CodeBlock
        className="max-h-96 overflow-auto"
        code={formatRuntimeToolSchema(schema)}
        language="json"
      />
    </div>
  );
}
