import assert from 'node:assert/strict'
import test from 'node:test'
import { projectActivity } from '../../frontend/src/management/activity.ts'
import { projectTranscript } from '../../frontend/src/management/timeline.ts'
import type { Event, ModelProgress } from '../../frontend/src/management/schema.ts'

const event = (sequence: number, kind: string, data: unknown, attempt?: string): Event => ({ tool_attempt_id: attempt, id: `e${sequence}`, thread_id: 'main', sequence, generation: 1, kind, data, created_at: `2026-10-10T00:00:${String(sequence).padStart(2, '0')}Z` })

test('reasoning-only previews keep explicit progress and incomplete outcomes inside work groups', () => {
  for (const [state, status] of [['running', '正在输出'], ['cancelled', '已取消'], ['failed', '模型请求失败'], ['unknown', '结果未知'], ['settling', '同步最终状态']]) {
    const preview: ModelProgress = { attempt_id: 'attempt', turn_id: 'turn', generation: 1, sequence: 2, state, model: 'fixture:model', started_at: '', updated_at: '', snapshot: { revision: 1, truncated: false, blocks: [{ kind: 'reasoning', index: 0, text: 'Partial thought' }] } }
    const rows = projectActivity(projectTranscript([], [preview]), [])
    assert.equal(rows.length, 1)
    if (rows[0].kind !== 'activity' || rows[0].items[0].kind !== 'reasoning') assert.fail('missing thought')
    assert.match(rows[0].items[0].status, new RegExp(status))
  }
})

test('cancellation retains unknown operation evidence and marks pending delivery cancelled', () => {
  const calls = [{ type: 'tool_use', tool_use_id: 'unknown', tool_name: 'exec' }, { type: 'tool_use', tool_use_id: 'pending', tool_name: 'read' }]
  const events = [event(1, 'turn.started', { turn_id: 'turn' }), event(2, 'message.appended', { id: 'attempt', role: 'assistant', blocks: calls }),
    event(3, 'tool.unknown', { id: 'op1', turn_id: 'turn', call: calls[0], result: { content: 'Verify original operation' } }, 'attempt'),
    event(4, 'tool.cancelled', { id: 'op1', turn_id: 'turn', call: calls[0], result: { content: 'Cancellation pending', is_error: true } }, 'attempt'),
    event(5, 'tool.cancelled', { id: 'op2', turn_id: 'turn', call: calls[1], result: { content: 'Cancellation pending', is_error: true } }, 'attempt'),
    event(6, 'message.appended', { role: 'user', kind: 'tool_result', blocks: calls.map(call => ({ type: 'tool_result', tool_use_id: call.tool_use_id, content: 'Cancellation pending', is_error: true })) }, 'attempt'),
    event(7, 'thread.cancelled', {})]
  const tools = projectActivity(projectTranscript(events), events).flatMap(row => row.kind === 'activity' ? row.items.filter(item => item.kind === 'tool') : [])
  assert.deepEqual(tools.map(tool => tool.state), ['unknown', 'cancelled'])
  assert.equal(tools[0].result?.content, 'Verify original operation')
})

test('late receipts follow operation identity when a later attempt reuses the call ID', () => {
  const call = { type: 'tool_use', tool_use_id: 'same', tool_name: 'mcp_connect' }
  const events = [event(1, 'turn.started', { turn_id: 'turn' }), event(2, 'message.appended', { id: 'first', role: 'assistant', blocks: [call] }),
    event(3, 'tool.ready', { id: 'op1', turn_id: 'turn', call, result: { content: 'Connected' } }, 'first'),
    event(4, 'message.appended', { id: 'second', role: 'assistant', blocks: [{ ...call, tool_name: 'read' }] }),
    event(5, 'tool.ready', { id: 'op2', turn_id: 'turn', call, result: { content: 'File contents' } }, 'second'),
    event(6, 'tool.unknown', { id: 'op1', turn_id: 'turn', call, result: { content: 'Connection outcome unknown' } }, 'first')]
  const tools = projectActivity(projectTranscript(events), events).flatMap(row => row.kind === 'activity' ? row.items.filter(item => item.kind === 'tool') : [])
  assert.deepEqual(tools.map(tool => [tool.state, tool.result?.content]), [['unknown', 'Connection outcome unknown'], ['completed', 'File contents']])
  const partial = events.slice(4)
  const retained = projectActivity(projectTranscript(partial), partial).flatMap(row => row.kind === 'activity' ? row.items.filter(item => item.kind === 'tool') : [])
  assert.deepEqual(retained.map(tool => [tool.operationID, tool.state, tool.result?.content]), [['op2', 'completed', 'File contents'], ['op1', 'unknown', 'Connection outcome unknown']])
  const interruptedWindow = [events[3], { ...events[5], sequence: 5 }, { ...events[4], sequence: 6 }]
  const interrupted = projectActivity(projectTranscript(interruptedWindow), interruptedWindow).flatMap(row => row.kind === 'activity' ? row.items.filter(item => item.kind === 'tool') : [])
  assert.deepEqual(interrupted.map(tool => [tool.operationID, tool.state]), [['op2', 'completed'], ['op1', 'unknown']])
})

test('same-name parallel tools pair by call identity in one work disclosure with prose outside', () => {
  const events = [
    event(1, 'turn.started', { turn_id: 'turn' }),
    event(2, 'message.appended', { id: 'calls', role: 'assistant', model: 'primary', blocks: [{ type: 'reasoning', text: 'Plan' }, { type: 'tool_use', tool_use_id: 'a', tool_name: 'read', input: { path: 'a' } }, { type: 'tool_use', tool_use_id: 'b', tool_name: 'read', input: { path: 'b' } }] }),
    event(3, 'tool.ready', { id: 'ob', turn_id: 'turn', call: { tool_use_id: 'b' }, result: { type: 'tool_result', tool_use_id: 'b', content: 'B', is_error: false } }, 'calls'),
    event(4, 'message.appended', { id: 'result', kind: 'tool_result', role: 'user', blocks: [{ type: 'tool_result', tool_use_id: 'a', tool_name: 'read', content: 'A' }, { type: 'tool_result', tool_use_id: 'b', tool_name: 'read', content: 'B' }] }, 'calls'),
    event(5, 'message.appended', { id: 'final', role: 'assistant', model: 'fallback', blocks: [{ type: 'text', text: 'Answer' }] }),
  ]
  const items = projectActivity(projectTranscript(events), events)
  assert.equal(items.length, 2)
  if (items[0].kind !== 'activity') assert.fail('missing work group')
  const tools = items[0].items.filter(item => item.kind === 'tool')
  assert.deepEqual(tools.map(tool => [tool.call?.tool_use_id, tool.result?.content]), [['a', 'A'], ['b', 'B']])
  assert.ok(tools.every(tool => tool.state === 'completed'))
  assert.equal(items[1].kind, 'message')
  if (items[1].kind !== 'message') assert.fail('missing prose')
  assert.equal(items[1].message.model, 'fallback')
  assert.equal(items[1].createdAt, '2026-10-10T00:00:05Z')
})

test('tool IDs reused in later Turns never replace earlier output', () => {
  const events = [
    event(1, 'turn.started', { turn_id: 'first' }),
    event(2, 'message.appended', { id: 'first', role: 'assistant', blocks: [{ type: 'tool_use', tool_use_id: 'same', tool_name: 'read' }] }),
    event(3, 'message.appended', { role: 'user', kind: 'tool_result', blocks: [{ type: 'tool_result', tool_use_id: 'same', content: 'first result' }] }, 'first'),
    event(4, 'turn.completed', { turn_id: 'first' }),
    event(5, 'turn.started', { turn_id: 'second' }),
    event(6, 'message.appended', { id: 'second', role: 'assistant', blocks: [{ type: 'tool_use', tool_use_id: 'same', tool_name: 'read' }] }),
    event(7, 'tool.unknown', { id: 'unknown', turn_id: 'second', call: { tool_use_id: 'same' }, result: { type: 'tool_result', tool_use_id: 'same', content: 'Inspect original operation' } }, 'second'),
  ]
  const groups = projectActivity(projectTranscript(events), events).filter(row => row.kind === 'activity')
  assert.equal(groups.length, 2)
  const first = groups[0].items[0], second = groups[1].items[0]
  if (first.kind !== 'tool' || second.kind !== 'tool') assert.fail('missing tools')
  assert.equal(first.result?.content, 'first result')
  assert.equal(first.state, 'completed')
  assert.equal(second.state, 'unknown')
})

test('orphan historical tool results stay inspectable and user text splits work groups', () => {
  const events = [
    event(1, 'message.appended', { kind: 'tool_result', role: 'user', blocks: [{ type: 'tool_result', tool_use_id: 'missing', tool_name: 'read', content: 'Retained output' }] }),
    event(2, 'message.appended', { role: 'user', blocks: [{ type: 'text', text: 'Next request' }] }),
    event(3, 'message.appended', { role: 'assistant', blocks: [{ type: 'reasoning', text: 'Next thought' }] }),
  ]
  const items = projectActivity(projectTranscript(events), events)
  assert.deepEqual(items.map(item => item.kind), ['activity', 'message', 'activity'])
  if (items[0].kind !== 'activity' || items[0].items[0].kind !== 'tool') assert.fail('missing output')
  assert.equal(items[0].items[0].result?.content, 'Retained output')
})

test('receipt-only windows display durable call details and later prepend reconciles once', () => {
  const call = { type: 'tool_use', tool_use_id: 'call', tool_name: 'exec', input: { command: 'work' } }
  for (const [kind, state] of [['tool.ready', 'completed'], ['tool.unknown', 'unknown'], ['tool.cancelled', 'cancelled']]) {
    const receipt = event(3, kind, { id: 'operation', turn_id: 'turn', call, result: { type: 'tool_result', tool_use_id: 'call', content: 'Retained result' } }, 'request')
    const older = [event(1, 'turn.started', { turn_id: 'turn' }), event(2, 'message.appended', { id: 'request', role: 'assistant', blocks: [call] })]
    for (const events of [[receipt], [...older, receipt]]) {
      const tools = projectActivity(projectTranscript(events), events).flatMap(row => row.kind === 'activity' ? row.items.filter(item => item.kind === 'tool') : [])
      assert.equal(tools.length, 1)
      assert.equal(tools[0].state, state)
      assert.equal(tools[0].result?.content, 'Retained result')
      assert.equal(tools[0].call?.tool_name, 'exec')
      assert.ok([tools[0].id, ...(tools[0].aliases ?? [])].includes('operation:operation'))
    }
  }
})

test('orphan result identity remains an alias after its earlier request is loaded', () => {
  const call = { type: 'tool_use', tool_use_id: 'call', tool_name: 'read' }
  const events = [event(1, 'turn.started', { turn_id: 'turn' }), event(2, 'message.appended', { id: 'request', role: 'assistant', blocks: [call] }), { ...event(3, 'message.appended', { id: 'result', role: 'user', kind: 'tool_result', blocks: [{ type: 'tool_result', tool_use_id: 'call', content: 'Output' }] }, 'request'), turn_id: 'turn' }]
  const group = projectActivity(projectTranscript(events), events)[0]
  if (group.kind !== 'activity' || group.items[0].kind !== 'tool') assert.fail('missing tool')
  assert.ok(group.items[0].aliases?.includes('result:0'))
})

const canonicalEvent = (sequence: number, kind: string, data: unknown, extra = {}): Event => ({ id: `c${sequence}`, thread_id: 'main', generation: 1, sequence, kind, data, ...extra })
const canonicalCall = (sequence = 1, ids = ['call']) => canonicalEvent(sequence, 'message.appended', { id: `request-${sequence}`, role: 'assistant', blocks: ids.map(id => ({ type: 'tool_use', tool_use_id: id, tool_name: 'read', input: { path: id } })) })
const canonicalResult = (sequence = 2, id = 'call', extra = {}) => canonicalEvent(sequence, 'message.appended', { id: `result-${sequence}`, role: 'user', kind: 'tool_result', blocks: [{ type: 'tool_result', tool_use_id: id, tool_name: 'read', content: `output-${sequence}`, ...extra }] })
const canonicalTools = (events: Event[]) => projectActivity(projectTranscript(events), events).flatMap(row => row.kind === 'activity' ? row.items.filter(item => item.kind === 'tool') : [])

test('canonical parallel calls pair unique IDs without Runtime attempt metadata', () => {
  const tools = canonicalTools([canonicalCall(1, ['a', 'b']), canonicalResult(2, 'b', { is_error: true }), canonicalResult(3, 'a')])
  assert.deepEqual(tools.map(tool => [tool.call?.tool_use_id, tool.result?.content, tool.state]), [['a', 'output-3', 'completed'], ['b', 'output-2', 'failed']])
})

test('canonical result pagination preserves its alias when the missing call is prepended', () => {
  const result = canonicalResult()
  const orphan = canonicalTools([result])[0]
  assert.equal(orphan.call, undefined)
  const paired = canonicalTools([canonicalCall(), result])
  assert.equal(paired.length, 1)
  assert.ok(paired[0].aliases?.includes(orphan.id))
  assert.equal(paired[0].result?.content, orphan.result?.content)
})

test('canonical calls are consumed once and later model batches may reuse their IDs', () => {
  const tools = canonicalTools([canonicalCall(), canonicalResult(), canonicalResult(3), canonicalCall(4), canonicalResult(5)])
  assert.deepEqual(tools.map(tool => [!!tool.call, tool.result?.content]), [[true, 'output-2'], [false, 'output-3'], [true, 'output-5']])
})

test('duplicate canonical call IDs remain ambiguous instead of guessing', () => {
  const tools = canonicalTools([canonicalCall(1, ['call', 'call']), canonicalResult()])
  assert.deepEqual(tools.map(tool => tool.state), ['waiting', 'waiting', 'completed'])
  assert.equal(tools[2].call, undefined)
})

test('canonical fallback never replaces explicit attempt identity', () => {
  const tools = canonicalTools([canonicalCall(), { ...canonicalResult(), tool_attempt_id: 'unloaded-attempt' }])
  assert.equal(tools.length, 2)
  assert.equal(tools[0].result, undefined)
  assert.equal(tools[1].call, undefined)
})

test('canonical matching stops at ordinary inputs, assistant batches and explicit Turn boundaries', () => {
  const boundaries = [
    canonicalEvent(2, 'message.appended', { role: 'user', blocks: [{ type: 'text', text: 'A new request' }] }),
    canonicalEvent(2, 'input.accepted', { receipt: { id: 'input' }, text: 'A queued request' }),
    canonicalEvent(2, 'message.appended', { id: 'new-attempt', role: 'assistant', blocks: [{ type: 'text', text: 'A new response' }] }),
    ...['turn.started', 'turn.completed', 'turn.failed', 'turn.cancelled', 'thread.cancelled', 'context.reset', 'context.compacted'].map(kind => canonicalEvent(2, kind, { turn_id: 'other' })),
  ]
  for (const boundary of boundaries) {
    const tools = canonicalTools([canonicalCall(), boundary, canonicalResult(3)])
    assert.equal(tools.length, 2, boundary.kind)
    assert.equal(tools[0].result, undefined, boundary.kind)
    assert.equal(tools[1].call, undefined, boundary.kind)
  }
})

test('canonical matching never crosses generation or row Turn identity', () => {
  for (const extra of [{ generation: 2 }, { turn_id: 'different-turn' }]) {
    const tools = canonicalTools([{ ...canonicalCall(), turn_id: 'original-turn' }, { ...canonicalResult(), ...extra }])
    assert.equal(tools.length, 2)
    assert.equal(tools[1].call, undefined)
  }
})

test('canonical result does not weaken an authoritative unknown or cancelled receipt', () => {
  for (const state of ['unknown', 'cancelled']) {
    const receipt = canonicalEvent(2, `tool.${state}`, { id: 'operation', call: { tool_use_id: 'call' }, result: { content: 'Authoritative receipt', is_error: true } }, { tool_attempt_id: 'request-1' })
    const tools = canonicalTools([canonicalCall(), receipt, canonicalResult(3)])
    assert.equal(tools.length, 1)
    assert.equal(tools[0].state, state)
    assert.equal(tools[0].result?.content, 'Authoritative receipt')
  }
})
