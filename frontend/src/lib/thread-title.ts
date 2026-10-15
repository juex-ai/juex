export function threadTitle(alias?: string | null, id?: string | null): string {
  const name = alias?.trim() || "Thread";
  return id ? `${name} · #${id}` : name;
}
