import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { api, errorText } from './api'
import type { OperationOutput } from './schema'

export type OperationHandle = { environment_id: string; operation_id: string }

export function operationHandle(content?: string): OperationHandle | null {
  if (!content) return null
  try {
    const value = JSON.parse(content)
    return typeof value?.handle?.environment_id === 'string' && typeof value.handle.operation_id === 'string' && value.handle.environment_id && value.handle.operation_id ? value.handle : null
  } catch { return null }
}

const stateNames: Record<string, string> = { waiting: '等待环境', dispatched: '等待设备接收', accepted: '已接收', running: '正在运行', completed: '已结束', failed: '失败', cancelled: '已取消', unknown: '结果未知' }

// This is a view of the original operation. Reading never starts, resumes or
// acknowledges it, and does not replace the immutable model-facing tool result.
export function LiveOperationOutput({ base, handle }: { base: string; handle: OperationHandle }) {
  const [revision, setRevision] = useState(0)
  const [value, setValue] = useState<{ text: string; output?: OperationOutput; clipped?: boolean; error?: string }>({ text: '' })
  useEffect(() => {
    const controller = new AbortController()
    let timer: number | undefined
    let cursor = 0
    let bytes = new Uint8Array()
    let clipped = false
    const poll = async () => {
      try {
        const output = await api<OperationOutput>(`${base}/environments/${encodeURIComponent(handle.environment_id)}/operations/${encodeURIComponent(handle.operation_id)}/output?after=${cursor}`, undefined, undefined, controller.signal)
        if (controller.signal.aborted) return
        const chunk = Uint8Array.from(atob(output.output ?? ''), char => char.charCodeAt(0))
        if (output.next_cursor !== cursor + chunk.length && !output.output_expired) throw new Error('输出游标发生变化，请重新读取原操作。')
        const next = new Uint8Array(bytes.length + chunk.length); next.set(bytes); next.set(chunk, bytes.length)
        if (next.length > 256 * 1024) clipped = true
        bytes = next.slice(-256 * 1024)
        // The beginning or end may cut through a UTF-8 sequence. Wait for the
        // final continuation byte, and omit a clipped leading continuation.
        let start = 0
        while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
        const terminal = ['completed', 'failed', 'cancelled', 'unknown'].includes(output.state) && output.next_cursor >= output.output_bytes
        const text = new TextDecoder().decode(bytes.subarray(start), { stream: !terminal })
        cursor = output.next_cursor
        setValue({ text, output, clipped })
        if (!terminal && !output.output_expired) timer = window.setTimeout(() => void poll(), chunk.length === 64 * 1024 ? 0 : 750)
      } catch (error) { if (!controller.signal.aborted) setValue(previous => ({ ...previous, error: errorText(error) })) }
    }
    void poll()
    return () => { controller.abort(); window.clearTimeout(timer) }
  }, [base, handle.environment_id, handle.operation_id, revision])
  return <section className="management-operation-output" aria-label="原操作实时输出"><div className="management-log-label">原操作 · {value.output ? stateNames[value.output.state] ?? value.output.state : '正在读取'}{value.output?.exit_code != null && ` · 退出码 ${value.output.exit_code}`}</div>
    <pre>{value.text || '暂无输出'}</pre>
    {value.clipped && <small>显示最近 256 KiB 输出。</small>}
    {value.output?.truncated && <p>执行端输出已达到保留上限，后续内容不完整。</p>}
    {value.output?.output_expired && <p>此操作的输出已过保留期；不会重新运行它。</p>}
    {value.output?.error && <p>{value.output.error}</p>}
    {value.output?.state === 'unknown' && <p>操作结果未知，请核对原环境，避免重复执行。</p>}
    {value.error && <p role="status">{value.error}<Button size="sm" variant="ghost" onClick={() => setRevision(number => number + 1)}>重试读取输出</Button></p>}
  </section>
}
