import type { Event, InputImage, InputReceipt, Message, ModelProgress, Timeline } from './schema'

export type TranscriptRow = ({ kind: 'message'; id: string; message: Message; status: string; observationIDs?: string[] } | { kind: 'notice'; id: string; text: string } | { kind: 'hook'; id: string; hook: string; event: string; state: string; detail: string }) & { createdAt?: string; turnID?: string; sequence?: number; toolAttemptID?: string }

export function reconcileProgress(previous: ModelProgress[], page: Timeline): ModelProgress[] {
  const current = page.progress ?? []
  if (!page.has_more) return current
  const known = new Set(current.map(item => item.attempt_id))
  const settled = new Set(page.events.filter(event => event.kind === 'message.appended').map(event => (event.data as Message).id))
  // Successful completion removes the server preview before a later event page
  // reaches the browser. Keep already visible text until that page is loaded.
  return [...current, ...previous.filter(item => item.generation === page.thread.generation && !known.has(item.attempt_id) && !settled.has(item.attempt_id)).map(item => ({ ...item, state: 'settling' }))]
}

export function projectTranscript(events: Event[], progress: ModelProgress[] = []): TranscriptRow[] {
  const rows: TranscriptRow[] = []
  const messages = new Map<string, Extract<TranscriptRow, { kind: 'message' }>>()
  const seen = new Set<string>()
  const held = new Set<string>()
  const hooks = new Map<string, Extract<TranscriptRow, { kind: 'hook' }>>()
  let turnID = ''
  for (const event of events) {
    if (seen.has(event.id)) continue
    seen.add(event.id)
    const start = rows.length
    const data = event.data as { receipt?: InputReceipt; text?: string; images?: InputImage[]; input_id?: string; reason?: string; error?: string; to_model?: string; turn_id?: string; source?: { kind?: string } }
    if (event.turn_id !== undefined) turnID = event.turn_id
    if (event.kind === 'turn.started') turnID = data.turn_id ?? ''
    if (event.kind === 'input.accepted' && data.receipt && typeof data.text === 'string') {
      const row: Extract<TranscriptRow, { kind: 'message' }> = { kind: 'message', id: data.receipt.id, message: { id: data.receipt.id, role: 'user', kind: data.source?.kind === 'application_trigger' ? 'system_notice' : undefined, blocks: [{ type: 'text', text: data.text }] }, status: '已接收，等待执行' }
      for (const media of data.images ?? []) row.message.blocks.push({ type: 'image', media: { artifact_id: media.artifact_id, sha256: media.sha256, media_type: media.media_type, original_bytes: media.size } })
      rows.push(row); messages.set(row.id, row)
    } else if (event.kind === 'message.appended') {
      const message = event.data as Message
      const id = message.id ?? event.id
      const existing = messages.get(id)
      if (existing) { existing.message = message; existing.status = ''; existing.turnID = turnID; existing.sequence = event.sequence; existing.createdAt = event.created_at; existing.observationIDs = event.observation_ids } else {
        const row: Extract<TranscriptRow, { kind: 'message' }> = { kind: 'message', id, message, status: '', observationIDs: event.observation_ids }
        rows.push(row); messages.set(id, row)
      }
    } else if (event.kind.startsWith('hook.')) {
      const hook = event.data as { id: string; hook_id: string; event: string; result?: { output?: { stdout: string; stderr: string }; error: string } }
      const row: Extract<TranscriptRow, { kind: 'hook' }> = { kind: 'hook', id: hook.id, hook: hook.hook_id, event: hook.event, state: event.kind.slice(5), detail: [hook.result?.output?.stdout, hook.result?.output?.stderr, hook.result?.error].filter(Boolean).join('\n') }
      const existing = hooks.get(hook.id)
      if (existing) Object.assign(existing, row)
      else { hooks.set(hook.id, row); rows.push(row) }
    } else if (event.kind === 'input.held' && data.input_id) {
      held.add(data.input_id)
      const row = messages.get(data.input_id)
      const status = data.reason === 'instructions_unavailable' ? '指令文件读取未完成，本轮已暂停；检查执行环境或来源设置后请重新提交' : data.reason === 'compaction_failed' ? '上下文压缩未完成，本轮已暂停；原始内容保留' : data.reason === 'context_limit' ? '上下文超出可用模型容量，本轮已暂停' : data.reason === 'model_unavailable' ? '模型不可用，已暂停；配置后请重新提交' : data.reason === 'media_unavailable' ? '历史图片不可用或超过请求容量，本轮已暂停；请检查附件' : '授权已改变，未继续执行'
      if (row) row.status = status
      else rows.push({ kind: 'notice', id: event.id, text: status })
    } else if (event.kind === 'instructions.failed' || event.kind === 'instructions.unknown') {
      rows.push({ kind: 'notice', id: event.id, text: `指令文件读取未完成${data.error ? `：${data.error}` : '。请检查执行环境和来源设置。'}` })
    } else if (event.kind === 'context.requested') {
      rows.push({ kind: 'notice', id: event.id, text: '上下文压缩请求已接收，将按顺序执行。' })
    } else if (event.kind === 'context.compacting') {
      rows.push({ kind: 'notice', id: event.id, text: '正在整理上下文摘要…' })
    } else if (event.kind === 'context.compacted') {
      rows.push({ kind: 'notice', id: event.id, text: '上下文已压缩，完整历史仍然保留。' })
    } else if (event.kind === 'context.reset') {
      rows.push({ kind: 'notice', id: event.id, text: '新上下文 · 后续消息从此处开始，完整历史仍然保留。' })
    } else if (event.kind === 'context.unchanged') {
      rows.push({ kind: 'notice', id: event.id, text: '当前上下文较短，无需压缩。' })
    } else if (event.kind === 'model.fallback') {
      const reason = data.reason === 'context_limit' ? '上下文超出模型容量' : data.reason === 'model_unavailable' ? '模型已不可用或授权已变更' : '模型请求失败'
      rows.push({ kind: 'notice', id: event.id, text: `${reason}，已切换到预设备用模型${data.to_model ? ` ${data.to_model}` : ''}。` })
    } else if (event.kind === 'turn.failed') {
      rows.push({ kind: 'notice', id: event.id, text: data.error === 'invalid_response' ? '模型返回了无法处理的响应。请检查模型配置后重试。' : data.error?.toLowerCase().includes('hook') ? `Hook 阻止了本轮继续：${data.error}` : '模型请求失败，本轮已停止。已接收的输入和历史仍然保留。' })
    } else if (event.kind === 'thread.cancelled' || event.kind === 'turn.cancelled') {
      for (const row of messages.values()) if (row.status === '已接收，等待执行' || event.kind === 'thread.cancelled' && held.has(row.id)) row.status = '已取消'
      if (event.kind === 'thread.cancelled') held.clear()
      rows.push({ kind: 'notice', id: event.id, text: '已取消本次对话。已经开始的外部操作可能仍在结束中。' })
    } else if (event.kind === 'tool.unknown') {
      rows.push({ kind: 'notice', id: event.id, text: '无法确认外部操作的结果，对话已暂停。请先核对设备上的实际状态；停止本轮后，可以发送新的处理指令。' })
    } else if (event.kind === 'memory.recall_unavailable') {
      rows.push({ kind: 'notice', id: event.id, text: '本轮未能取得共享记忆参考，对话将继续。' })
    } else if (event.kind === 'turn.recovered') {
      rows.push({ kind: 'notice', id: event.id, text: '服务已恢复，正在继续原来的对话。' })
    }
    for (const row of rows.slice(start)) { row.createdAt = event.created_at; row.turnID = turnID; row.sequence = event.sequence; row.toolAttemptID = event.tool_attempt_id }
  }
  for (const preview of progress) {
    if (messages.has(preview.attempt_id) || !preview.snapshot.blocks.length) continue
    const outcome: Record<string, string> = { running: '正在输出…', settling: '正在同步最终状态…', failed: '未完成 · 模型请求失败', cancelled: '未完成 · 已取消', unknown: '未完成 · 服务中断，结果未知' }
    rows.push({ kind: 'message', id: preview.attempt_id, turnID: preview.turn_id, sequence: preview.sequence, createdAt: preview.started_at,
      message: { id: preview.attempt_id, role: 'assistant', model: preview.model, blocks: preview.snapshot.blocks.map(block => ({ type: block.kind, text: block.text })) },
      status: `${outcome[preview.state] ?? '未完成'}${preview.snapshot.truncated ? ' · 预览已达容量上限，完整结果完成后显示' : ''}` })
  }
  return rows.sort((a, b) => (a.sequence ?? 0) - (b.sequence ?? 0))
}
