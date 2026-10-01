import assert from 'node:assert/strict'
import { test } from 'node:test'
import { correctMemoryEntry } from '../../frontend/src/management/memory-correction.ts'
import type { MemoryEntry } from '../../frontend/src/management/schema.ts'

test('human correction preserves the original assertion and creates one replacement', () => {
  const old: MemoryEntry = {
    id: 'preferences', revision: 4, name: 'Preference', summary: 'Response format', type: 'user', scope: {}, body: '', sources: [], created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    facts: [{ id: 'prior', domain: 'preferences', subject: 'user', predicate: 'prefers', value: 'three points', status: 'valid', reason: 'User requested', sources: [], source_type: 'user_statement', recorded_at: '2026-01-01T00:00:00Z' }],
  }
  const edited = structuredClone(old)
  edited.facts![0].value = 'two points'
  const result = correctMemoryEntry(old, edited, '2026-10-01T00:00:00Z', () => 'replacement')
  assert.equal(result.revision, 4)
  assert.deepEqual(result.facts?.[0], { ...old.facts![0], status: 'corrected' })
  assert.equal(result.facts?.[1].value, 'two points')
  assert.equal(result.facts?.[1].recorded_at, '2026-10-01T00:00:00Z')
  assert.deepEqual(result.facts?.[1].replaces, ['prior'])
  assert.equal(old.facts![0].status, 'valid')
  assert.deepEqual(correctMemoryEntry(old, old, 'unused', () => { throw Error('unchanged assertion replaced') }), old)
})
