import type { ModuleState, ThreadModulesSnapshot } from "../module-schema";
import type { FileContribution, ModuleContribution, StatusContribution } from "./types";

export function resolveContributions(snapshot: ThreadModulesSnapshot | undefined, registry: readonly ModuleContribution[]) {
  const status: { definition: StatusContribution; state: ModuleState }[] = [];
  const files: FileContribution[] = [];
  const diagnostics: string[] = [];
  const seen = new Set<string>();
  for (const ui of snapshot?.ui ?? []) {
    if (seen.has(ui.id)) continue;
    seen.add(ui.id);
    const definition = registry.find((item) => item.id === ui.id);
    const state = snapshot?.modules[ui.module_id];
    if (!definition || definition.moduleID !== ui.module_id || definition.version !== ui.version ||
      !state || state.module_id !== ui.module_id || state.version !== definition.version) {
      diagnostics.push(`${ui.id} v${ui.version} unavailable: unsupported contribution`);
      continue;
    }
    if (state.status !== "ready") {
      diagnostics.push(`${definition.label} unavailable: ${state.error || state.status}`);
      continue;
    }
    if (definition.slot === "thread.status") status.push({ definition, state });
    else if (state.resources?.includes(definition.resource)) files.push(definition);
    else diagnostics.push(`${definition.label} unavailable: missing resource`);
  }
  const compare = (a: ModuleContribution, b: ModuleContribution) => a.order - b.order || a.id.localeCompare(b.id);
  status.sort((a, b) => compare(a.definition, b.definition));
  files.sort(compare);
  return { status, files, diagnostics };
}
