import type { Block, Event } from './schema'
import type { TranscriptRow } from './timeline'

export type ToolActivity = { kind: 'tool'; id: string; operationID?: string; aliases?: string[]; call?: Block; result?: Block; state: 'waiting' | 'completed' | 'failed' | 'cancelled' | 'unknown'; startedAt?: string; finishedAt?: string }
export type Activity = ToolActivity | { kind: 'reasoning'; id: string; text: string; status: string } | Extract<TranscriptRow, { kind: 'hook' }>
export type DisplayRow = TranscriptRow | { kind: 'activity'; id: string; turnID?: string; items: Activity[] }

export const activityKeys = (item: Activity) => [item.id, ...(item.kind === 'tool' ? item.aliases ?? [] : [])]

export function projectActivity(rows: TranscriptRow[], events: Event[]): DisplayRow[] {
  const result: DisplayRow[] = []
  const tools = new Map<string, ToolActivity>()
  const operations = new Map<string, ToolActivity>()
  const receipts = new Set<ToolActivity>()
  const batch = new Map<string, ToolActivity | null>()
  const boundaries = new Set(['turn.started', 'turn.completed', 'turn.failed', 'turn.cancelled', 'thread.cancelled', 'context.reset', 'context.compacted'])
  let generation: number | undefined
  let turnID: string | undefined
  const turnTools = new Map<string, ToolActivity[]>()
  const key = (turn: string | undefined, call: string) => `${turn ?? ''}:${call}`
  const append = (row: TranscriptRow, activity: Activity) => {
    const previous = result.at(-1)
    if (previous?.kind === 'activity' && previous.turnID === row.turnID) previous.items.push(activity)
    else result.push({ kind: 'activity', id: `work:${activity.id}`, turnID: row.turnID, items: [activity] })
  }
  const nodes = [
    ...rows.map(row => ({ sequence: row.sequence ?? 0, row, event: null })),
    ...events.filter(event => ['tool.ready', 'tool.unknown', 'tool.cancelled'].includes(event.kind) || boundaries.has(event.kind)).map(event => ({ sequence: event.sequence, row: null, event })),
  ].sort((a, b) => a.sequence - b.sequence)
  for (const node of nodes) {
    const scopeGeneration = node.event?.generation ?? node.row?.generation
    const scopeTurn = node.row ? node.row.turnID : node.event?.turn_id ?? (node.event?.data as { turn_id?: string }).turn_id ?? turnID
    if (generation !== scopeGeneration || turnID !== scopeTurn) batch.clear()
    generation = scopeGeneration; turnID = scopeTurn
    if (node.event) {
      const event = node.event
      if (boundaries.has(event.kind)) batch.clear()
      const data = event.data as { id?: string; turn_id?: string; call?: Block; result?: Block }
      if (event.kind === 'thread.cancelled' || event.kind === 'turn.cancelled') {
        const candidates = event.kind === 'thread.cancelled' ? [...tools.values()] : turnTools.get(data.turn_id ?? '') ?? []
        for (const tool of candidates) if (tool.state === 'waiting') { tool.state = 'cancelled'; tool.finishedAt = event.created_at }
      } else if (data.call?.tool_use_id) {
        const candidate = event.tool_attempt_id ? tools.get(key(event.tool_attempt_id, data.call.tool_use_id)) : undefined
        let tool = (data.id ? operations.get(data.id) : undefined) ?? (candidate && (!candidate.operationID || candidate.operationID === data.id) ? candidate : undefined)
        if (!tool) {
          tool = { kind: 'tool', id: data.id ? `operation:${data.id}` : event.id, call: data.call, state: 'waiting' }
          if (!candidate && event.tool_attempt_id) tools.set(key(event.tool_attempt_id, data.call.tool_use_id), tool)
          append({ kind: 'notice', id: event.id, text: '', turnID: data.turn_id, sequence: event.sequence }, tool)
        }
        if (tool) {
          if (data.id) { operations.set(data.id, tool); tool.operationID = data.id; tool.aliases = [...new Set([...(tool.aliases ?? []), `operation:${data.id}`])] }
          tool.call ??= data.call
          receipts.add(tool)
          // Conversation cancellation cannot settle an unknown external action.
          if (event.kind === 'tool.cancelled' && tool.state === 'unknown') continue
          tool.result = data.result; tool.finishedAt = event.created_at
          tool.state = event.kind === 'tool.unknown' ? 'unknown' : event.kind === 'tool.cancelled' ? 'cancelled' : data.result?.is_error ? 'failed' : 'completed'
        }
      }
      continue
    }
    const row = node.row!
    if (row.kind === 'hook') { append(row, row); continue }
    if (row.kind !== 'message' || ['compact', 'system_notice'].includes(row.message.kind ?? '')) { result.push(row); continue }
    // Canonical history need not have Runtime operations. Match only within a
    // single assistant batch; explicit attempt receipts remain authoritative.
    const resultsOnly = row.message.role === 'user' && row.message.blocks.length > 0 && row.message.blocks.every(block => block.type === 'tool_result')
    if (row.message.role === 'assistant' || row.message.role === 'user' && !resultsOnly) batch.clear()
    let visible: Block[] = []
    const flush = (index: number) => {
      if (visible.length) { result.push({ ...row, id: `${row.id}:body:${index}`, message: { ...row.message, blocks: visible } }); visible = [] }
    }
    row.message.blocks.forEach((block, index) => {
      const id = `${row.id}:${index}`
      if (block.type === 'reasoning') { flush(index); append(row, { kind: 'reasoning', id, text: block.text || '此部分未提供可显示的内容。', status: row.status }) }
      else if (block.type === 'tool_use') {
        flush(index)
        const tool: ToolActivity = { kind: 'tool', id, call: block, state: 'waiting', startedAt: row.createdAt }
        if (block.tool_use_id) batch.set(block.tool_use_id, batch.has(block.tool_use_id) ? null : tool)
        if (block.tool_use_id && row.message.id) tools.set(key(row.message.id, block.tool_use_id), tool)
        const list = turnTools.get(row.turnID ?? '') ?? []; list.push(tool); turnTools.set(row.turnID ?? '', list)
        append(row, tool)
      } else if (block.type === 'tool_result') {
        flush(index)
        const tool = block.tool_use_id ? row.toolAttemptID ? tools.get(key(row.toolAttemptID, block.tool_use_id)) : batch.get(block.tool_use_id) : undefined
        if (block.tool_use_id && tool && batch.get(block.tool_use_id) === tool) batch.delete(block.tool_use_id)
        if (tool) { tool.aliases = [...new Set([...(tool.aliases ?? []), id])]; if (!receipts.has(tool)) { tool.result = block; tool.state = block.is_error ? 'failed' : 'completed'; tool.finishedAt ??= row.createdAt } }
        else {
          const orphan: ToolActivity = { kind: 'tool', id, result: block, state: block.is_error ? 'failed' : 'completed', finishedAt: row.createdAt }
          if (block.tool_use_id && row.toolAttemptID) tools.set(key(row.toolAttemptID, block.tool_use_id), orphan)
          append(row, orphan)
        }
      } else visible.push(block)
    })
    flush(row.message.blocks.length)
  }
  return result
}
