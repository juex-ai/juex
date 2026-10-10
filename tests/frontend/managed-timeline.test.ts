import assert from 'node:assert/strict'
import test from 'node:test'
import { projectTranscript, reconcileProgress } from '../../frontend/src/management/timeline.ts'

test('instruction read failures explain the held input without claiming authorization changed', () => {
  const rows = projectTranscript([
    event(1, 'input.accepted', { receipt: { id: 'guidance-input' }, text: 'Continue' }),
    event(2, 'instructions.failed', { error: 'instruction source must contain bounded UTF-8 text' }),
    event(3, 'input.held', { input_id: 'guidance-input', reason: 'instructions_unavailable' }),
  ])
  assert.ok(rows.some(row => row.kind === 'notice' && row.text.includes('UTF-8')))
  assert.ok(rows.some(row => row.kind === 'message' && row.status.includes('指令文件')))
  assert.ok(!rows.some(row => row.kind === 'message' && row.status.includes('授权已改变')))
})
import type { Event, ModelProgress, Thread, Timeline } from '../../frontend/src/management/schema.ts'

test('forward event pagination retains displayed previews until final reconciliation, without claiming they are still running', () => {
  const preview: ModelProgress = { attempt_id: 'attempt', turn_id: 'turn', generation: 1, sequence: 450, model: 'fixture:model', state: 'running', started_at: '', updated_at: '', snapshot: { revision: 1, truncated: false, blocks: [{ kind: 'text', index: 0, text: 'Visible output' }] } }
  const page: Timeline = { thread: { generation: 1 } as Thread, events: [event(400, 'turn.started', {})], next_sequence: 400, has_more: true, progress: [] }
  const catchingUp = reconcileProgress([preview], page)
  assert.equal(catchingUp[0]?.snapshot.blocks[0].text, 'Visible output')
  assert.equal(catchingUp[0]?.state, 'settling')
  assert.deepEqual(reconcileProgress(catchingUp, { ...page, events: [event(452, 'message.appended', { id: 'attempt' })] }), [])
  assert.deepEqual(reconcileProgress(catchingUp, { ...page, has_more: false }), [])
  assert.deepEqual(reconcileProgress(catchingUp, { ...page, thread: { generation: 2 } as Thread }), [])
})

test('streamed previews stay provisional and final messages replace them exactly once', () => {
  const preview: ModelProgress = { attempt_id: 'attempt', turn_id: 'turn', generation: 1, sequence: 3, model: 'fixture:model', state: 'running', started_at: '2026-10-10T00:00:00Z', updated_at: '2026-10-10T00:00:01Z', snapshot: { revision: 1, truncated: false, blocks: [{ kind: 'text', index: 0, text: 'Partial' }] } }
  const events = [event(1, 'input.accepted', { receipt: { id: 'user' }, text: 'Question' }), event(2, 'turn.started', { turn_id: 'turn' })]
  const live = projectTranscript(events, [preview])
  assert.equal(live.length, 2)
  if (live[1].kind !== 'message') assert.fail('missing preview')
  assert.equal(live[1].message.blocks[0].text, 'Partial')
  assert.match(live[1].status, /正在输出/)
  const cancelled = projectTranscript(events, [{ ...preview, state: 'cancelled' }])
  if (cancelled[1].kind !== 'message') assert.fail('missing cancelled preview')
  assert.match(cancelled[1].status, /未完成.*已取消/)
  const final = projectTranscript([...events, event(4, 'message.appended', { id: 'attempt', role: 'assistant', blocks: [{ type: 'text', text: 'Complete answer' }] })], [preview])
  assert.equal(final.length, 2)
  if (final[1].kind !== 'message') assert.fail('missing final')
  assert.equal(final[1].message.blocks[0].text, 'Complete answer')
  assert.equal(final[1].status, '')
})

function event(sequence: number, kind: string, data: unknown): Event {
  return { id: `event-${sequence}`, thread_id: 'thread', sequence, generation: 1, kind, data, created_at: '2026-09-30T00:00:00Z' }
}

test('loading the earlier input receipt never moves an already displayed consumed message ahead of another answer', () => {
  const receipt = event(1, 'input.accepted', { receipt: { id: 'queued' }, text: 'Next work' })
  const answer = event(2, 'message.appended', { id: 'earlier-answer', role: 'assistant', blocks: [{ type: 'text', text: 'Previous answer' }] })
  const consumed = { ...event(3, 'message.appended', { id: 'queued', role: 'user', blocks: [{ type: 'text', text: 'Next work' }] }), created_at: '2026-09-30T01:00:00Z' }
  assert.deepEqual(projectTranscript([receipt, answer, consumed]), projectTranscript([answer, consumed]))
})

test('queued Calendar triggers retain system provenance before consumption', () => {
  const accepted = event(1, 'input.accepted', { receipt: { id: 'calendar-input' }, text: 'Calendar trigger: Check context', source: { kind: 'application_trigger', application: 'calendar' } })
  const queued = projectTranscript([accepted])
  if (queued[0].kind !== 'message') assert.fail('missing queued trigger')
  assert.equal(queued[0].message.kind, 'system_notice')
  assert.equal(queued[0].status, '已接收，等待执行')
  const consumed = projectTranscript([accepted, event(2, 'message.appended', { id: 'calendar-input', role: 'user', kind: 'system_notice', blocks: [{ type: 'text', text: 'Calendar trigger: Check context' }] })])
  assert.equal(consumed.length, 1)
  if (consumed[0].kind !== 'message') assert.fail('missing consumed trigger')
  assert.equal(consumed[0].message.kind, 'system_notice')
  assert.equal(consumed[0].status, '')
})

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

test('explicit cancellation settles held input without rewriting a completed reply', () => {
  const rows = projectTranscript([
    event(1, 'input.accepted', { receipt: { id: 'held-input' }, text: 'original work' }),
    event(2, 'input.held', { input_id: 'held-input', reason: 'model_unavailable' }),
    event(3, 'message.appended', { id: 'reply', role: 'assistant', blocks: [{ type: 'text', text: 'Completed replacement' }] }),
    event(4, 'thread.cancelled', {}),
  ])
  if (rows[0].kind !== 'message' || rows[1].kind !== 'message') assert.fail('missing history')
  assert.equal(rows[0].status, '已取消')
  assert.equal(rows[1].status, '')
  assert.equal(rows[1].message.blocks[0].text, 'Completed replacement')
})

test('cancelling a later Turn leaves unrelated held inputs unresolved', () => {
  const rows = projectTranscript([
    event(1, 'input.accepted', { receipt: { id: 'held-input' }, text: 'original work' }),
    event(2, 'input.held', { input_id: 'held-input', reason: 'model_unavailable' }),
    event(3, 'turn.cancelled', { input_id: 'different-input' }),
  ])
  if (rows[0].kind !== 'message') assert.fail('missing held input')
  assert.match(rows[0].status, /模型不可用/)
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

test('unavailable image pauses input with an attachment-specific explanation', () => {
  const rows = projectTranscript([
    event(1, 'input.accepted', { receipt: { id: 'input' }, text: 'Describe this image' }),
    event(2, 'input.held', { input_id: 'input', reason: 'media_unavailable' }),
  ])
  if (rows[0].kind !== 'message') assert.fail('missing held input')
  assert.match(rows[0].status, /图片.*暂停/)
  assert.doesNotMatch(rows[0].status, /授权已改变/)
})

test('a durable context reset remains a visible boundary between old and new messages', () => {
  const rows = projectTranscript([
    event(1, 'message.appended', { id: 'old', role: 'user', blocks: [{ type: 'text', text: 'old context' }] }),
    { ...event(2, 'context.reset', { generation: 2 }), generation: 2 },
    { ...event(3, 'message.appended', { id: 'new', role: 'assistant', blocks: [{ type: 'text', text: 'new context' }] }), generation: 2 },
  ])
  assert.equal(rows.length, 3)
  assert.equal(rows[1].kind, 'notice')
  if (rows[1].kind === 'notice') assert.match(rows[1].text, /新上下文.*历史/)
})
