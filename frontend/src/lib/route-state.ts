import { agentBasePath } from "./fleet-routes.ts";

export function isHistoryPath(pathname: string): boolean {
  const base = agentBasePath(pathname);
  return pathname === "/history" || (base !== "" && pathname === `${base}/history`);
}
