export function isHistoryPath(pathname: string): boolean {
  return pathname === "/history" || pathname.startsWith("/history/");
}
