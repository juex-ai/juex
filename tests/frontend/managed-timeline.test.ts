import assert from 'node:assert/strict'
import test from 'node:test'
import { projectTranscript } from '../../frontend/src/management/timeline.ts'
import type { Event } from '../../frontend/src/management/schema.ts'

function event(sequence: number, kind: string, data: unknown): Event {
  return { id: `event-${sequence}`, thread_id: 'thread', sequence, generation: 1, kind, data, created_at: '2026-09-30T00:00:00Z' }
}

test('managed timeline reconciles a receipt with its committed message without duplication', () => {
  const accepted = event(1, 'input.accepted', { receipt: { id: 'input-1', request_id: 'request-1' }, text: 'hello' })
  const rows = projectTranscript([accepted, accepted, event(2, 'message.appended', { id: 'input-1', role: 'user', blocks: [{ type: 'text', text: 'hello' }] }), event(3, 'message.appended', { id: 'answer', role: 'assistant', blocks: [{ type: 'text', text: 'reply' }] })])
  assert.equal(rows.length, 2)
  assert.equal(rows[0].kind, 'message')
  if (rows[0].kind === 'message') assert.equal(rows[0].status, '')
  assert.equal(rows[1].id, 'answer')
})

test('held and cancelled queued inputs keep the original user text and explicit outcome', () => {
  const accepted = event(1, 'input.accepted', { receipt: { id: 'input-1' }, text: 'work' })
  const held = projectTranscript([accepted, event(2, 'input.held', { input_id: 'input-1', reason: 'model_unavailable' })])
  assert.equal(held.length, 1)
  assert.equal(held[0].kind, 'message')
  if (held[0].kind === 'message') assert.match(held[0].status, /模型不可用/)
  const cancelled = projectTranscript([accepted, event(2, 'thread.cancelled', {})])
  if (cancelled[0].kind === 'message') assert.equal(cancelled[0].status, '已取消')
  assert.equal(cancelled[1].kind, 'notice')
})

test('model fallback and context exhaustion show distinct durable outcomes', () => {
  const rows = projectTranscript([
    event(1, 'input.accepted', { receipt: { id: 'input' }, text: 'work' }),
    event(2, 'model.fallback', { reason: 'model_unavailable', to_model: 'operator:backup' }),
    event(3, 'input.held', { input_id: 'input', reason: 'context_limit' }),
  ])
  assert.equal(rows.length, 2)
  if (rows[0].kind === 'message') assert.match(rows[0].status, /上下文/)
  if (rows[1].kind === 'notice') assert.match(rows[1].text, /授权已变更.*operator:backup/)
})
