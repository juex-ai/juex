import { useCallback, useEffect, useState } from "react";
import { RefreshCw, Save } from "lucide-react";
import { useParams } from "react-router-dom";

import { getAgentConfig, updateAgentConfig } from "@/api";
import { useShellTitle } from "@/components/AppShell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { resolveConfigSaveFailure } from "@/lib/agent-config";
import { cn } from "@/lib/utils";
import type { AgentConfig as AgentConfigState } from "@/types";

export function AgentConfig() {
  const { agentId = "" } = useParams<{ agentId: string }>();
  const [config, setConfig] = useState<AgentConfigState | null>(null);
  const [content, setContent] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  useShellTitle("Config");

  const refresh = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    setError(null);
    setNotice(null);
    try {
      const next = await getAgentConfig(agentId);
      setConfig(next);
      setContent(next.content);
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Failed to load agent config.",
      );
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function save() {
    if (!agentId) return;
    const submittedContent = content;
    setSaving(true);
    setError(null);
    setNotice(null);
    try {
      const result = await updateAgentConfig(agentId, submittedContent);
      setConfig(result.config);
      setContent(result.config.content);
      setNotice(
        `Saved and restarted ${result.agent.name || result.agent.id}.`,
      );
    } catch (cause) {
      const detail =
        cause instanceof Error ? cause.message : "Unknown configuration error.";
      let persistedConfig: AgentConfigState | null = null;
      try {
        persistedConfig = await getAgentConfig(agentId);
      } catch {
        // Keep the submitted content when persistence cannot be confirmed.
      }
      const failure = resolveConfigSaveFailure(
        submittedContent,
        persistedConfig,
        detail,
      );
      if (failure.config) {
        setConfig(failure.config);
      }
      if (failure.saved && failure.config) {
        setContent(failure.config.content);
      }
      setError(failure.message);
    } finally {
      setSaving(false);
    }
  }

  const dirty = config !== null && content !== config.content;
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-4 py-6 md:px-6">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="text-xl font-semibold text-foreground">
                Agent config
              </h1>
              {config ? (
                <Badge variant="outline" className="font-mono text-[10px]">
                  {config.exists ? "existing" : "new"}
                </Badge>
              ) : null}
            </div>
            <p
              className="mt-1 truncate font-mono text-xs text-muted-foreground"
              title={config?.path}
            >
              {config?.path || agentId}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void refresh()}
              disabled={loading || saving}
            >
              <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
              Reload
            </Button>
            <Button
              type="button"
              size="sm"
              onClick={() => void save()}
              disabled={loading || saving || !dirty}
            >
              <Save className="size-3.5" />
              {saving ? "Saving..." : "Save and restart"}
            </Button>
          </div>
        </div>

        {error ? (
          <div
            role="alert"
            className="rounded-md border border-destructive/45 bg-destructive/10 px-3 py-2 text-sm font-medium text-destructive"
          >
            {error}
          </div>
        ) : null}
        {notice ? (
          <div
            role="status"
            className="rounded-md border border-emerald-600/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700"
          >
            {notice}
          </div>
        ) : null}

        <Textarea
          value={content}
          onChange={(event) => setContent(event.target.value)}
          disabled={loading || config === null}
          aria-label="Agent juex.yaml"
          aria-invalid={error ? true : undefined}
          spellCheck={false}
          className="min-h-[32rem] resize-y rounded-md bg-card font-mono text-xs leading-5 shadow-[var(--shadow-xs)]"
          placeholder={loading ? "Loading config..." : "model: provider:model"}
        />
      </div>
    </div>
  );
}
