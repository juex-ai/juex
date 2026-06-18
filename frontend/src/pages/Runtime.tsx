import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { getRuntimeStatus } from "@/api";
import type {
  RuntimeHookInfo,
  RuntimeStatusResponse,
  SystemPromptEntry,
  WorkingStateRecord,
} from "@/types";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import {
  WORKING_STATE_SECTIONS,
  formatRuntimeTimestamp,
  formatRuntimeTokenCount,
  runtimeHookCommandLabel,
  runtimeHooksSummaryLabel,
  workingStatePresenceLabel,
  workingStateRecords,
  workingStateSectionCounts,
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
  const workingState = data.working_state;
  const workingStateCounts = workingStateSectionCounts(workingState);

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

        {data.goal ? (
          <section className="space-y-3">
            <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
              <h1 className="font-serif text-2xl italic leading-none text-primary">
                Goal
              </h1>
              <Badge variant={goalStatusVariant(data.goal.status)} className="font-mono text-[11px]">
                {data.goal.status || "unknown"}
              </Badge>
            </div>
            <div className="overflow-hidden rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
              <dl className="grid gap-0 text-sm sm:grid-cols-[9rem_minmax(0,1fr)]">
                <dt className="border-b bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground sm:border-b-0">
                  Objective
                </dt>
                <dd className="break-words px-3 py-2">
                  {data.goal.objective || "-"}
                </dd>
                <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                  Updated
                </dt>
                <dd className="border-t px-3 py-2 font-mono text-xs">
                  {formatRuntimeTimestamp(data.goal.updated_at)}
                </dd>
                <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                  Progress
                </dt>
                <dd className="border-t break-words px-3 py-2">
                  {data.goal.last_progress || "-"}
                </dd>
                <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                  Check
                </dt>
                <dd className="border-t px-3 py-2">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <Badge variant={goalStatusVariant(data.goal.last_check?.status)} className="font-mono text-[11px]">
                      {data.goal.last_check?.status || "none"}
                    </Badge>
                    <span className="min-w-0 break-words">
                      {data.goal.last_check?.summary || "-"}
                    </span>
                  </div>
                </dd>
                {data.goal.last_check?.continue_prompt ? (
                  <>
                    <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                      Continue
                    </dt>
                    <dd className="border-t break-words px-3 py-2">
                      {data.goal.last_check.continue_prompt}
                    </dd>
                  </>
                ) : null}
                {data.goal.blocked_reason ? (
                  <>
                    <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                      Blocked
                    </dt>
                    <dd className="border-t break-words px-3 py-2">
                      {data.goal.blocked_reason}
                    </dd>
                  </>
                ) : null}
                {data.goal.next_user_input ? (
                  <>
                    <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                      User input
                    </dt>
                    <dd className="border-t break-words px-3 py-2">
                      {data.goal.next_user_input}
                    </dd>
                  </>
                ) : null}
                <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                  Budget
                </dt>
                <dd className="border-t px-3 py-2 font-mono text-xs">
                  {goalBudgetLabel(data)}
                </dd>
                <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                  Evidence
                </dt>
                <dd className="border-t px-3 py-2">
                  {data.goal.evidence && data.goal.evidence.length > 0 ? (
                    <ul className="space-y-2">
                      {data.goal.evidence.map((item, index) => (
                        <li
                          key={item.id || index}
                          className="border-t py-2 first:border-t-0"
                        >
                          <div className="flex min-w-0 flex-wrap items-center gap-2">
                            {item.kind ? (
                              <Badge variant="outline" className="font-mono text-[11px]">
                                {item.kind}
                              </Badge>
                            ) : null}
                            {item.source ? (
                              <Badge variant="secondary" className="font-mono text-[11px]">
                                {item.source}
                              </Badge>
                            ) : null}
                            <span className="min-w-0 break-words text-sm">
                              {item.text || "-"}
                            </span>
                          </div>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <span className="text-muted-foreground">No evidence.</span>
                  )}
                </dd>
              </dl>
            </div>
          </section>
        ) : null}

        <section className="space-y-3">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
            <h1 className="font-serif text-2xl italic leading-none text-primary">
              Working State
            </h1>
            <Badge
              variant={workingState?.disabled ? "destructive" : "secondary"}
              className="font-mono text-[11px]"
            >
              {workingStatePresenceLabel(workingState)}
            </Badge>
          </div>
          <div className="overflow-hidden rounded-[14px] border bg-card shadow-[var(--shadow-sm)]">
            {workingState ? (
              <>
                <dl className="grid gap-0 text-sm sm:grid-cols-[9rem_minmax(0,1fr)]">
                  <dt className="border-b bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground sm:border-b-0">
                    Path
                  </dt>
                  <dd className="break-all px-3 py-2 font-mono text-xs">
                    {workingState.path || "-"}
                  </dd>
                  <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Updated
                  </dt>
                  <dd className="border-t px-3 py-2 font-mono text-xs">
                    {formatRuntimeTimestamp(workingState.state.updated_at)}
                  </dd>
                  <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
                    Version
                  </dt>
                  <dd className="border-t px-3 py-2 font-mono text-xs">
                    {workingState.state.version || 1}
                  </dd>
                </dl>
                <div className="border-t bg-muted/20 px-3 py-3">
                  <div className="grid gap-y-3 sm:grid-cols-2 lg:grid-cols-4">
                    {workingStateCounts.map((item) => (
                      <div
                        key={item.key}
                        className="border-l px-3 first:border-l-0 sm:[&:nth-child(2n+1)]:border-l-0 lg:[&:nth-child(2n+1)]:border-l lg:[&:nth-child(4n+1)]:border-l-0"
                      >
                        <div className="text-muted-foreground text-[11px] uppercase tracking-[0.14em]">
                          {item.label}
                        </div>
                        <div className="mt-1 font-mono text-lg leading-none">
                          {item.count}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
                {WORKING_STATE_SECTIONS.map((section) => (
                  <WorkingStateSection
                    key={section.key}
                    label={section.label}
                    records={workingStateRecords(workingState.state, section.key)}
                  />
                ))}
              </>
            ) : (
              <div className="text-muted-foreground px-3 py-3 text-sm">
                No active primary session.
              </div>
            )}
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

function WorkingStateSection({
  label,
  records,
}: {
  label: string;
  records: WorkingStateRecord[];
}) {
  if (records.length === 0) return null;
  return (
    <div className="border-t px-3 py-3">
      <div className="mb-2 flex min-w-0 flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{label}</span>
        <Badge variant="secondary" className="font-mono text-[11px]">
          {records.length}
        </Badge>
      </div>
      <ul className="space-y-2">
        {records.slice(0, 4).map((record, index) => (
          <WorkingStateRecordRow record={record} key={record.id || index} />
        ))}
      </ul>
      {records.length > 4 ? (
        <div className="text-muted-foreground mt-2 text-xs">
          {records.length - 4} more records
        </div>
      ) : null}
    </div>
  );
}

function WorkingStateRecordRow({ record }: { record: WorkingStateRecord }) {
  return (
    <li className="border-t py-2 first:border-t-0">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        {record.severity ? (
          <Badge
            variant={record.severity === "critical" || record.severity === "high" ? "destructive" : "outline"}
            className="font-mono text-[11px]"
          >
            {record.severity}
          </Badge>
        ) : null}
        {record.source ? (
          <Badge variant="outline" className="font-mono text-[11px]">
            {record.source}
          </Badge>
        ) : null}
        {record.confidence ? (
          <Badge variant="secondary" className="font-mono text-[11px]">
            {Math.round(record.confidence * 100)}%
          </Badge>
        ) : null}
        <span className="min-w-0 break-words text-sm">
          {record.text || record.id || "-"}
        </span>
      </div>
      {record.related_paths && record.related_paths.length > 0 ? (
        <div className="text-muted-foreground mt-2 break-all font-mono text-xs">
          {record.related_paths.join(", ")}
        </div>
      ) : null}
      {record.created_at || record.resolved_at ? (
        <div className="text-muted-foreground mt-2 font-mono text-xs">
          {record.created_at ? `created ${formatRuntimeTimestamp(record.created_at)}` : ""}
          {record.created_at && record.resolved_at ? " · " : ""}
          {record.resolved_at ? `resolved ${formatRuntimeTimestamp(record.resolved_at)}` : ""}
        </div>
      ) : null}
    </li>
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

function goalStatusVariant(status?: string): "secondary" | "outline" | "destructive" {
  if (status === "complete") return "secondary";
  if (status === "blocked") return "destructive";
  return "outline";
}

function goalBudgetLabel(data: RuntimeStatusResponse): string {
  const budget = data.goal?.budget;
  if (!budget) return "-";
  const used = budget.continuations_used ?? 0;
  const max = budget.max_continuations ?? 0;
  return max > 0 ? `${used}/${max} continuations` : `${used} continuations`;
}
