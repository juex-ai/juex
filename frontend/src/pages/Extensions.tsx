import { useEffect, useState } from "react";

import { getRuntimeStatus } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import { Badge } from "@/components/ui/badge";
import type { ExtensionEnvironmentVariable, ExtensionInfo, RuntimeStatusResponse } from "@/types";

export function Extensions() {
  const [data, setData] = useState<RuntimeStatusResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  useShellTitle("Extensions");

  useEffect(() => {
    let live = true;
    let timer: number | undefined;
    const load = () => {
      getRuntimeStatus()
        .then((status) => {
          if (!live) return;
          setData(status);
          setError(null);
        })
        .catch((cause) => {
          console.error("getRuntimeStatus failed", cause);
          if (live) {
            setError(cause instanceof Error ? cause.message : String(cause));
          }
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

  if (error && !data) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center p-6">
        <div
          role="alert"
          className="w-full max-w-xl rounded-lg border border-destructive/25 bg-destructive/10 px-4 py-3 text-sm text-destructive"
        >
          <p className="font-medium">Extensions are unavailable.</p>
          <p className="mt-1 break-words font-mono text-xs">{error}</p>
        </div>
      </div>
    );
  }
  if (!data) {
    return <LoadingState label="Loading extensions" />;
  }

  const extensions = data.extensions?.items ?? [];
  return (
    <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain bg-background">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-4 py-5 md:px-6 md:py-6">
        <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-xl font-semibold text-foreground">Extensions</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              Selected runtime bundles and their contributed resources.
            </p>
          </div>
          <Badge variant="secondary" className="font-mono text-[11px]">
            {extensions.length}
          </Badge>
        </div>
        {extensions.length === 0 ? (
          <div className="rounded-lg border bg-card px-4 py-8 text-center text-sm text-muted-foreground shadow-[var(--shadow-sm)]">
            No Extensions are selected for this Agent.
          </div>
        ) : (
          <div className="space-y-4">
            {extensions.map((extension) => (
              <ExtensionCard key={extension.name} extension={extension} />
            ))}
          </div>
        )}
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            Refresh failed: {error}
          </p>
        ) : null}
      </div>
    </div>
  );
}

function ExtensionCard({ extension }: { extension: ExtensionInfo }) {
  const resources = extension.resources;
  const environment = extension.environment ?? [];
  return (
    <section className="overflow-hidden rounded-lg border bg-card shadow-[var(--shadow-sm)]">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3 border-b px-4 py-3">
        <div className="min-w-0">
          <h3 className="break-words text-base font-semibold text-foreground">
            {extension.display_name || extension.name}
          </h3>
          <p className="mt-1 break-words text-sm text-muted-foreground">
            {extension.description || "No description provided."}
          </p>
        </div>
        <Badge variant="outline" className="font-mono text-[11px]">
          v{extension.version}
        </Badge>
      </div>
      <dl className="grid text-sm sm:grid-cols-[10rem_minmax(0,1fr)]">
        <Metadata label="Name" value={extension.name} />
        <Metadata label="Source" value={scopeLabel(extension.scope)} />
        <Metadata label="Install path" value={extension.path} />
        <Metadata label="Manifest" value={`version ${extension.manifest_version}`} />
        <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          Resources
        </dt>
        <dd className="flex flex-wrap gap-2 border-t px-3 py-2">
          <ResourceBadge label="Skills" count={resources.skills} />
          <ResourceBadge label="MCP" count={resources.mcp_servers} />
          <ResourceBadge label="Hooks" count={resources.hooks} />
          <ResourceBadge label="Observables" count={resources.observables} />
        </dd>
      </dl>
      {environment.length > 0 ? (
        <div className="border-t px-4 py-3">
          <h4 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            Agent environment
          </h4>
          <div className="mt-2 space-y-2">
            {environment.map((variable) => (
              <EnvironmentVariable key={`${variable.source}:${variable.name}`} variable={variable} />
            ))}
          </div>
        </div>
      ) : null}
    </section>
  );
}

function EnvironmentVariable({ variable }: { variable: ExtensionEnvironmentVariable }) {
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2 rounded-md bg-muted/50 px-3 py-2 text-xs">
      <code className="break-all font-mono text-foreground">{variable.name}</code>
      <Badge variant="outline" className="font-mono text-[10px]">
        {variable.status}
      </Badge>
      <span className="break-all text-muted-foreground">{variable.source}</span>
      {variable.status === "shadowed" && variable.shadowed_by_source ? (
        <span className="break-all text-muted-foreground">
          shadowed by {variable.shadowed_by_source}
        </span>
      ) : null}
    </div>
  );
}

function Metadata({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="border-t bg-muted/60 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground first:border-t-0 sm:first:border-t-0">
        {label}
      </dt>
      <dd className="break-all border-t px-3 py-2 font-mono text-xs first:border-t-0 sm:[&:nth-child(2)]:border-t-0">
        {value || "-"}
      </dd>
    </>
  );
}

function ResourceBadge({ label, count }: { label: string; count: number }) {
  return (
    <Badge variant="outline" className="font-mono text-[11px]">
      {label} {count}
    </Badge>
  );
}

function scopeLabel(scope: ExtensionInfo["scope"]): string {
  switch (scope) {
    case "default_home":
      return "Default Home";
    case "instance_home":
      return "Instance Home";
    case "project":
      return "Project";
    default:
      return scope;
  }
}
