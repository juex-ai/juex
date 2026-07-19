import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { useParams } from "react-router-dom";

import { getAgentLogs } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { LoadingState } from "@/components/LoadingState";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

const lineOptions = [100, 200, 500, 1000];

export function AgentLogs() {
  const { agentId = "" } = useParams<{ agentId: string }>();
  const [lines, setLines] = useState(200);
  const [content, setContent] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  useShellTitle("Logs");

  const refresh = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    setError(null);
    try {
      setContent(await getAgentLogs(agentId, lines));
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Failed to load agent logs.",
      );
    } finally {
      setLoading(false);
    }
  }, [agentId, lines]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <h1 className="text-xl font-semibold text-foreground">Agent logs</h1>
            <p className="mt-1 break-all font-mono text-xs text-muted-foreground">
              {agentId}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Select
              value={String(lines)}
              onValueChange={(value) => setLines(Number(value))}
            >
              <SelectTrigger size="sm" aria-label="Log line count">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {lineOptions.map((count) => (
                  <SelectItem key={count} value={String(count)}>
                    {count} lines
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void refresh()}
              disabled={loading}
            >
              <RefreshCw
                className={cn(
                  "size-3.5 motion-reduce:animate-none",
                  loading && "animate-spin",
                )}
              />
              Refresh
            </Button>
          </div>
        </div>
        {error ? (
          <div
            role="alert"
            className="rounded-md border border-destructive/35 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </div>
        ) : null}
        {loading && !content ? (
          <LoadingState
            label="Loading logs"
            className="min-h-[24rem] rounded-md border bg-card"
          />
        ) : (
          <pre className="min-h-[24rem] overflow-auto rounded-md border bg-card p-4 font-mono text-xs leading-5 text-foreground shadow-[var(--shadow-xs)]">
            {content || "No log output in the selected tail."}
          </pre>
        )}
      </div>
    </div>
  );
}
