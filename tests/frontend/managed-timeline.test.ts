import assert from 'node:assert/strict'
import test from 'node:test'
import { projectTranscript } from '../../frontend/src/management/timeline.ts'
import type { Event } from '../../frontend/src/management/schema.ts'

function event(sequence: number, kind: string, data: unknown): Event {
  return { id: `event-${sequence}`, thread_id: 'thread', sequence, generation: 1, kind, data, created_at: '2026-09-30T00:00:00Z' }
}

test('hook completion replaces its pending log while preserving an unknown result', () => {
  const rows = projectTranscript([
    event(1, 'hook.started', { id: 'hook-operation', hook_id: 'guard', event: 'PreToolUse' }),
    event(2, 'hook.unknown', { id: 'hook-operation', hook_id: 'guard', event: 'PreToolUse', result: { error: 'Do not repeat', output: { stdout: 'partial', stderr: '' } } }),
  ])
  assert.equal(rows.length, 1)
  if (rows[0].kind !== 'hook') assert.fail('missing hook log')
  assert.equal(rows[0].state, 'unknown')
  assert.equal(rows[0].detail, 'partial\nDo not repeat')
})

test('optional Memory failure is a notice and leaves user input intact', () => {
  const rows = projectTranscript([
    event(1, 'input.accepted', { receipt: { id: 'input' }, text: 'continue' }),
    event(2, 'memory.recall_unavailable', { input_id: 'input' }),
  ])
  assert.equal(rows.length, 2)
  if (rows[0].kind === 'message') assert.equal(rows[0].status, '已接收，等待执行')
  assert.equal(rows[1].kind, 'notice')
  if (rows[1].kind === 'notice') assert.match(rows[1].text, /对话将继续/)
})

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

test('compaction control and failures stay separate from user messages', () => {
  const rows = projectTranscript([
    event(1, 'context.requested', { receipt: { id: 'control' }, text: '' }),
    event(2, 'context.compacting', {}),
    event(3, 'input.held', { input_id: 'control', reason: 'compaction_failed' }),
    event(4, 'context.unchanged', {}),
  ])
  assert.equal(rows.length, 4)
  assert.ok(rows.every(row => row.kind === 'notice'))
  if (rows[2].kind === 'notice') assert.match(rows[2].text, /未完成.*原始内容保留/)
  if (rows[3].kind === 'notice') assert.match(rows[3].text, /无需压缩/)
})
