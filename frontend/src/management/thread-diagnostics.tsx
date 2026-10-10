import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Empty, Failure, Loading } from './components'
import { useResource } from './use-resource'
import type { Event, Timeline } from './schema'

function eventDetails(event: Event) {
  const data = event.data && typeof event.data === 'object' ? event.data as Record<string, unknown> : {}
  // Project diagnostic metadata only. Tool arguments, responses and user input
  // already have their own views and can contain large or private content.
  const keys = ['error', 'reason', 'state', 'status', 'tool', 'name', 'model', 'from_model_id', 'to_model', 'operation_id', 'attempt_id']
  const details = keys.flatMap(key => typeof data[key] === 'string' && data[key] ? [[key, data[key] as string]] : [])
  const call = data.call && typeof data.call === 'object' ? data.call as Record<string, unknown> : {}
  const result = data.result && typeof data.result === 'object' ? data.result as Record<string, unknown> : {}
  if (typeof call.tool_name === 'string') details.push(['tool', call.tool_name])
  if (result.is_error === true) details.push(['error', '工具返回错误标记；可在对话中展开对应工具查看结果。'])
  return details
}

export function ThreadDiagnostics({ base, thread }: { base: string; thread: string }) {
  const [cursors, setCursors] = useState([0])
  const [revision, setRevision] = useState(0)
  const [problemsOnly, setProblemsOnly] = useState(false)
  const refresh = () => { setCursors([0]); setRevision(value => value + 1) }
  const resource = useResource<Timeline>(`${base}/threads/${thread}/events?before=${cursors[cursors.length - 1]}&limit=100`, revision)
  const entries = resource.data?.events.map(event => {
    const details = eventDetails(event)
    const problem = /failed|cancelled|unknown|held|fallback/.test(event.kind) || details.some(([key, value]) => key === 'error' || key === 'state' && /failed|unknown/.test(value))
    return { event, details, problem }
  }).filter(entry => !problemsOnly || entry.problem).reverse()
  return <section className="management-diagnostics" aria-label="Thread 诊断记录">
    <p>当前 Thread 的持久化事件，包含状态变化、模型切换和失败原因。每页最近 100 条，由新到旧；不是共享服务进程日志。</p>
    <div className="management-row-actions"><label><input type="checkbox" checked={problemsOnly} onChange={event => setProblemsOnly(event.target.checked)} />只看本页异常与模型切换</label><Button variant="outline" onClick={refresh}>刷新最新记录</Button></div>
    {resource.error ? <Failure message={resource.error} retry={() => setRevision(value => value + 1)} /> : !resource.data ? <Loading /> : <>
      {!entries?.length ? <Empty title={problemsOnly ? '本页没有匹配记录' : '暂无诊断记录'}>{resource.data.has_previous ? '可以继续查看更早记录。' : '只表示当前查询范围内没有匹配记录。'}</Empty> : <ol>{entries.map(({ event, details, problem }) => <li key={event.id} className={problem ? 'is-problem' : ''}>
        <div><strong>{event.kind}</strong><time dateTime={event.created_at}>{new Date(event.created_at).toLocaleString()}</time></div>
        <small>#{event.sequence} · 第 {event.generation} 代</small>
        {details.length > 0 && <dl>{details.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl>}
        <details><summary>关联标识</summary><p>事件 <code>{event.id}</code></p>{event.turn_id && <p>Turn <code>{event.turn_id}</code></p>}{event.tool_attempt_id && <p>工具尝试 <code>{event.tool_attempt_id}</code></p>}</details>
      </li>)}</ol>}
      <div className="management-memory-pagination"><Button variant="outline" disabled={cursors.length === 1} onClick={() => setCursors(value => value.slice(0, -1))}>较新记录</Button><span>第 {cursors.length} 页</span><Button variant="outline" disabled={!resource.data.has_previous || !resource.data.previous_sequence} onClick={() => setCursors(value => [...value, resource.data!.previous_sequence!])}>更早记录</Button></div>
    </>}
  </section>
}
