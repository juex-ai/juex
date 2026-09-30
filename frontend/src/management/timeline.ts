import type { Event, InputReceipt, Message } from './schema'

export type TranscriptRow = { kind: 'message'; id: string; message: Message; status: string } | { kind: 'notice'; id: string; text: string }

export function projectTranscript(events: Event[]): TranscriptRow[] {
  const rows: TranscriptRow[] = []
  const messages = new Map<string, Extract<TranscriptRow, { kind: 'message' }>>()
  const seen = new Set<string>()
  for (const event of events) {
    if (seen.has(event.id)) continue
    seen.add(event.id)
    const data = event.data as { receipt?: InputReceipt; text?: string; input_id?: string; reason?: string; error?: string; to_model?: string }
    if (event.kind === 'input.accepted' && data.receipt && typeof data.text === 'string') {
      const row: Extract<TranscriptRow, { kind: 'message' }> = { kind: 'message', id: data.receipt.id, message: { id: data.receipt.id, role: 'user', blocks: [{ type: 'text', text: data.text }] }, status: '已接收，等待执行' }
      rows.push(row); messages.set(row.id, row)
    } else if (event.kind === 'message.appended') {
      const message = event.data as Message
      const id = message.id ?? event.id
      const existing = messages.get(id)
      if (existing) { existing.message = message; existing.status = '' } else {
        const row: Extract<TranscriptRow, { kind: 'message' }> = { kind: 'message', id, message, status: '' }
        rows.push(row); messages.set(id, row)
      }
    } else if (event.kind === 'input.held' && data.input_id) {
      const row = messages.get(data.input_id)
      const status = data.reason === 'compaction_failed' ? '上下文压缩未完成，本轮已暂停；原始内容保留' : data.reason === 'context_limit' ? '上下文超出可用模型容量，本轮已暂停' : data.reason === 'model_unavailable' ? '模型不可用，已暂停；配置后请重新提交' : '授权已改变，未继续执行'
      if (row) row.status = status
      else rows.push({ kind: 'notice', id: event.id, text: status })
    } else if (event.kind === 'context.requested') {
      rows.push({ kind: 'notice', id: event.id, text: '上下文压缩请求已接收，将按顺序执行。' })
    } else if (event.kind === 'context.compacting') {
      rows.push({ kind: 'notice', id: event.id, text: '正在整理上下文摘要…' })
    } else if (event.kind === 'context.compacted') {
      rows.push({ kind: 'notice', id: event.id, text: '上下文已压缩，完整历史仍然保留。' })
    } else if (event.kind === 'context.unchanged') {
      rows.push({ kind: 'notice', id: event.id, text: '当前上下文较短，无需压缩。' })
    } else if (event.kind === 'model.fallback') {
      const reason = data.reason === 'context_limit' ? '上下文超出模型容量' : data.reason === 'model_unavailable' ? '模型已不可用或授权已变更' : '模型请求失败'
      rows.push({ kind: 'notice', id: event.id, text: `${reason}，已切换到预设备用模型${data.to_model ? ` ${data.to_model}` : ''}。` })
    } else if (event.kind === 'turn.failed') {
      rows.push({ kind: 'notice', id: event.id, text: data.error === 'invalid_response' ? '模型返回了无法处理的响应。请检查模型配置后重试。' : '模型请求失败，本轮已停止。已接收的输入和历史仍然保留。' })
    } else if (event.kind === 'thread.cancelled' || event.kind === 'turn.cancelled') {
      for (const row of messages.values()) if (row.status === '已接收，等待执行') row.status = '已取消'
      rows.push({ kind: 'notice', id: event.id, text: '已取消本次对话。已经开始的外部操作可能仍在结束中。' })
    } else if (event.kind === 'tool.unknown') {
      rows.push({ kind: 'notice', id: event.id, text: '无法确认外部操作的结果，对话已暂停。请先核对设备上的实际状态；停止本轮后，可以发送新的处理指令。' })
    } else if (event.kind === 'memory.recall_unavailable') {
      rows.push({ kind: 'notice', id: event.id, text: '本轮未能取得共享记忆参考，对话将继续。' })
    } else if (event.kind === 'turn.recovered') {
      rows.push({ kind: 'notice', id: event.id, text: '服务已恢复，正在继续原来的对话。' })
    }
  }
  return rows
}
