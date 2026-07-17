const AGENT_PREFIX = "/agents/";

export function agentBasePath(pathname: string): string {
  if (!pathname.startsWith(AGENT_PREFIX)) return "";
  const segment = pathname.slice(AGENT_PREFIX.length).split("/", 1)[0];
  return segment ? `${AGENT_PREFIX}${segment}` : "";
}

export function agentIDFromPath(pathname: string): string | null {
  const base = agentBasePath(pathname);
  if (!base) return null;
  const encoded = base.slice(AGENT_PREFIX.length);
  try {
    return decodeURIComponent(encoded);
  } catch {
    return null;
  }
}

export function agentPagePath(agentID: string, path = ""): string {
  const base = `${AGENT_PREFIX}${encodeURIComponent(agentID)}`;
  const suffix = normalizePath(path);
  return suffix === "/" ? base : `${base}${suffix}`;
}

export function agentPathFromLocation(
  path: string,
  pathname = browserPathname(),
): string {
  const suffix = normalizePath(path);
  const base = agentBasePath(pathname);
  return base ? `${base}${suffix === "/" ? "" : suffix}` : suffix;
}

export function agentSwitchPath(agentID: string, pathname: string): string {
  const base = agentPagePath(agentID);
  const currentBase = agentBasePath(pathname);
  if (!currentBase) return base;

  const suffix = pathname.slice(currentBase.length);
  if (suffix === "/runtime" || suffix === "/history" || suffix === "/logs" || suffix === "/config") {
    return `${base}${suffix}`;
  }
  if (suffix === "/observables" || suffix.startsWith("/observables/")) {
    return `${base}/observables`;
  }
  return base;
}

function normalizePath(path: string): string {
  const raw = path.trim();
  if (!raw || raw === "/") return "/";
  const withSlash = raw.startsWith("/") ? raw : `/${raw}`;
  return withSlash
    .split("/")
    .map((segment, index) => (index === 0 ? "" : normalizeSegment(segment)))
    .join("/");
}

function normalizeSegment(segment: string): string {
  try {
    return encodeURIComponent(decodeURIComponent(segment));
  } catch {
    return encodeURIComponent(segment);
  }
}

function browserPathname(): string {
  return typeof window === "undefined" ? "" : window.location.pathname;
}
