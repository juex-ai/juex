export interface RequestGeneration {
  current: number;
}

export function beginLatestRequest(
  generation: RequestGeneration,
): () => boolean {
  const request = ++generation.current;
  return () => request === generation.current;
}

export function invalidateLatestRequest(generation: RequestGeneration): void {
  generation.current += 1;
}
