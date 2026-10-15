export type RuntimeSection =
  | "overview"
  | "extensions"
  | "observables"
  | "logs"
  | "config";

export const runtimeSections: ReadonlyArray<{
  id: RuntimeSection;
  label: string;
}> = [
  { id: "overview", label: "Overview" },
  { id: "extensions", label: "Extensions" },
  { id: "observables", label: "Observables" },
  { id: "logs", label: "Logs" },
  { id: "config", label: "Config" },
];

export function runtimeSectionFromPath(pathname: string): RuntimeSection {
  const suffix = pathname.replace(/^\/agents\/[^/]+\/runtime\/?/, "");
  const section = suffix.split("/", 1)[0];
  switch (section) {
    case "extensions":
    case "observables":
    case "logs":
    case "config":
      return section;
    default:
      return "overview";
  }
}

export function runtimeSectionPath(
  agentID: string,
  section: RuntimeSection,
): string {
  const base = `/agents/${encodeURIComponent(agentID)}/runtime`;
  return section === "overview" ? base : `${base}/${section}`;
}
