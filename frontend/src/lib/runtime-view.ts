import type { RuntimeStatusResponse } from "../types.ts";

export interface RuntimeView {
  data: RuntimeStatusResponse;
  error: string | null;
  lastUpdated: Date | null;
}

export function runtimeModuleEnabled(data: RuntimeStatusResponse, id: string): boolean {
  return id === "extensions"
    ? data.extensions.enabled
    : data.modules.some((module) => module.id === id);
}
