import test from 'node:test'
import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'
import { randomUUID } from '../../frontend/src/lib/uuid.ts'

test('HTTP identifiers remain UUID v4 without secure-context crypto APIs', () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto')!
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) } })
  try {
    const values = Array.from({ length: 100 }, () => randomUUID())
    for (const value of values) assert.match(value, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    assert.equal(new Set(values).size, values.length)
  } finally {
    Object.defineProperty(globalThis, 'crypto', descriptor)
  }
})
