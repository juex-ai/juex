import type { MemoryEntry } from './schema'

// Facts are immutable assertions. A human correction keeps the original visible
// in history and creates a replacement instead of rewriting its identity.
export function correctMemoryEntry(original: MemoryEntry, edited: MemoryEntry, now: string, newID: () => string): MemoryEntry {
  const prior = new Map(original.facts?.map(fact => [fact.id, fact]))
  return { ...edited, facts: edited.facts?.flatMap(fact => {
    const old = prior.get(fact.id)
    if (!old || old.value === fact.value) return [fact]
    return [
      { ...old, status: 'corrected' },
      { ...fact, id: newID(), status: 'valid', replaces: [old.id], recorded_at: now, reason: 'Explicit human correction through the management dashboard.' },
    ]
  }) }
}
