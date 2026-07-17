import type { AgentConfig } from "@/types";

export interface ConfigSaveFailureResolution {
  config: AgentConfig | null;
  saved: boolean;
  message: string;
}

export function resolveConfigSaveFailure(
  submittedContent: string,
  persistedConfig: AgentConfig | null,
  detail: string,
): ConfigSaveFailureResolution {
  const saved =
    persistedConfig !== null && persistedConfig.content === submittedContent;
  return {
    config: persistedConfig,
    saved,
    message: saved
      ? `Config was saved, but the agent could not be restarted: ${detail}`
      : `Save or restart failed: ${detail}`,
  };
}
