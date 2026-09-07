import { useEffect, useState } from "react";
import { Outlet, useLocation, useNavigate, useParams } from "react-router-dom";
import { getRuntimeStatus } from "@/api";
import { LoadingState } from "@/components/LoadingState";
import { useFleetAgent } from "@/components/fleet/FleetAgentContext";
import { runtimeModuleEnabled } from "@/lib/runtime-view";
import type { RuntimeStatusResponse } from "@/types";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  runtimeSectionFromPath,
  runtimeSectionPath,
  runtimeSections,
  type RuntimeSection,
} from "@/lib/runtime-navigation";

export function RuntimeLayout() {
  const { agentId = "" } = useParams<{ agentId: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const section = runtimeSectionFromPath(location.pathname);
  const { resourceRevision } = useFleetAgent();
  const [snapshot, setSnapshot] = useState<{
    agentId: string;
    data: RuntimeStatusResponse;
  } | null>(null);
  const data = snapshot?.agentId === agentId ? snapshot.data : null;
  const [error, setError] = useState<string | null>(null);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  useEffect(() => {
    let live = true;
    void getRuntimeStatus()
      .then((status) => {
        if (!live) return;
        setSnapshot({ agentId, data: status });
        setError(null);
        setLastUpdated(new Date());
      })
      .catch((cause) => {
        if (live) setError(cause instanceof Error ? cause.message : String(cause));
      });
    return () => {
      live = false;
    };
  }, [agentId, resourceRevision.runtime]);

  const sectionEnabled = (id: RuntimeSection) =>
    (id !== "extensions" && id !== "observables") ||
    (data !== null && runtimeModuleEnabled(data, id));
  const needsCatalog = section !== "config" && section !== "logs";

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-background">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b bg-card px-4 py-3 md:px-6">
        <h1 className="font-serif text-xl italic leading-none text-primary">
          Runtime
        </h1>
        <Select
          value={section}
          onValueChange={(value) =>
            navigate(runtimeSectionPath(agentId, value as RuntimeSection))
          }
        >
          <SelectTrigger
            size="sm"
            className="min-w-36"
            aria-label="Runtime section"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent align="end">
            {runtimeSections.map((item) => (
              <SelectItem
                key={item.id}
                value={item.id}
                disabled={!sectionEnabled(item.id)}
              >
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {needsCatalog && !data ? (
        error ? (
          <p role="alert" className="p-6 text-sm text-destructive">
            Runtime status is unavailable: {error}
          </p>
        ) : (
          <LoadingState label="Loading runtime" />
        )
      ) : needsCatalog && !sectionEnabled(section) ? (
        <p className="p-6 text-sm text-muted-foreground">
          {section === "extensions" ? "Extensions" : "Observables"} module is disabled for this Agent.
        </p>
      ) : (
        <Outlet context={{ data, error, lastUpdated }} />
      )}
    </div>
  );
}
