import { createContext, useContext, useState, type ReactNode } from 'react'
import { Check, Copy, Wrench } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { writeClipboardText } from '@/lib/clipboard'
import { errorText } from './api'
import { hookEvents } from './hooks'
import { MessageMedia } from './message-media'
import { MessageMarkdown } from './message-markdown'
import { ObservationMessage } from './observation-message'
import { InputCheckMarker } from './input-checks'
import { LiveOperationOutput, operationHandle } from './operation-output'
import { activityKeys, type Activity, type DisplayRow, type ToolActivity } from './activity'
import type { Message } from './schema'

const toolState = { waiting: '等待结果', completed: '已返回', failed: '失败', cancelled: '已取消', unknown: '结果未知' }

const Expansions = createContext<{ open: Set<string>; change: (keys: string[], value: boolean) => void }>({ open: new Set(), change: () => {} })
const Visible = createContext(true)

export function TranscriptExpansion({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(new Set<string>())
  return <Expansions.Provider value={{ open, change: (keys, value) => setOpen(previous => { const next = new Set(previous); for (const key of keys) { if (value) next.add(key); else next.delete(key) } return next }) }}>{children}</Expansions.Provider>
}

function Disclosure({ keys, className, children }: { keys: string[]; className: string; children: ReactNode }) {
  const expansion = useContext(Expansions)
  const open = keys.some(key => expansion.open.has(key))
  const parent = useContext(Visible)
  return <details className={className} open={open} onToggle={event => { if (event.currentTarget.open !== open) expansion.change(keys, event.currentTarget.open) }}><Visible.Provider value={parent && open}>{children}</Visible.Provider></details>
}

function CopyMessage({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  return <><Button size="icon" variant="ghost" className="management-copy-message" aria-label={copied ? '已复制消息' : '复制消息'} onClick={() => { void writeClipboardText(text).then(() => { setCopied(true); setError('') }).catch(error => setError(errorText(error))) }}>{copied ? <Check size={13} /> : <Copy size={13} />}</Button>{error && <small role="status">{error}</small>}</>
}

function ToolView({ base, tool }: { base: string; tool: ToolActivity }) {
  const name = tool.call?.tool_name ?? tool.result?.tool_name ?? '工具'
  const elapsed = tool.startedAt && tool.finishedAt ? Date.parse(tool.finishedAt) - Date.parse(tool.startedAt) : NaN
  const keys = activityKeys(tool)
  const expansion = useContext(Expansions)
  const expanded = useContext(Visible) && keys.some(key => expansion.open.has(key))
  const handle = operationHandle(tool.result?.content)
  return <Disclosure keys={keys} className="management-tool-row"><summary data-transcript-anchors={JSON.stringify(keys)}><span>{tool.call ? name : `执行结果 · ${name}`}</span><span className={`management-tool-state state-${tool.state}`}>{toolState[tool.state]}</span>{Number.isFinite(elapsed) && elapsed >= 0 && <small title="工具请求到返回回执的间隔，不代表外部进程已退出">{(elapsed / 1000).toFixed(1)}s</small>}</summary>
    {tool.call && <><div className="management-log-label">请求</div><pre>{JSON.stringify(tool.call.input ?? {}, null, 2)}</pre></>}
    {tool.result && <><div className="management-log-label">输出</div><pre data-transcript-anchors={JSON.stringify(keys.map(key => `${key}:output`))}>{tool.result.content}</pre>{tool.result.media && <MessageMedia base={base} media={tool.result.media} />}</>}
    {expanded && handle && <LiveOperationOutput base={base} handle={handle} />}
    {tool.state === 'unknown' && <p>请先核对原操作的实际结果，避免重复执行。</p>}
  </Disclosure>
}

function ActivityView({ base, activity }: { base: string; activity: Activity }) {
  if (activity.kind === 'tool') return <ToolView base={base} tool={activity} />
  if (activity.kind === 'reasoning') return <Disclosure keys={[activity.id]} className="management-tool-row"><summary data-transcript-anchors={JSON.stringify([activity.id])}><span>思考过程</span>{activity.status && <small>{activity.status}</small>}</summary><p data-transcript-anchors={JSON.stringify([`${activity.id}:text`])}>{activity.text}</p></Disclosure>
  return <details className="management-hook-log"><summary>Hook · {activity.hook} · {hookEvents[activity.event] ?? activity.event} · {{ started: '等待执行结果', completed: '已完成', failed: '失败', cancelled: '已取消', unknown: '结果未知，请先核对设备状态' }[activity.state] ?? activity.state}</summary>{activity.detail && <pre>{activity.detail}</pre>}</details>
}

export function TranscriptItem({ base, row, application }: { base: string; row: DisplayRow; application?: string }) {
  if (row.kind === 'notice') return <p className="management-turn-notice">{row.text}</p>
  if (row.kind === 'hook') return <ActivityView base={base} activity={row} />
  if (row.kind === 'activity') {
    const tools = row.items.filter(item => item.kind === 'tool')
    const statuses = [...new Set(row.items.flatMap(item => item.kind === 'reasoning' && item.status ? [item.status] : []))]
    const keys = row.items.flatMap(activityKeys).map(key => `work:${key}`)
    return <Disclosure keys={keys} className="management-work-group"><summary data-transcript-anchors={JSON.stringify(keys)}><Wrench size={13} /><span>工作过程</span><small>{tools.length ? `${tools.length} 次工具调用` : `${row.items.length} 项记录`}</small>{statuses.map(status => <span key={status}>{status}</span>)}{tools.some(tool => tool.state === 'unknown') && <span>结果未知</span>}{tools.some(tool => tool.state === 'waiting') && <span>等待结果</span>}</summary><div>{row.items.map(activity => <ActivityView key={activity.id} base={base} activity={activity} />)}</div></Disclosure>
  }
  return <MessageView base={base} message={row.message} status={row.status} createdAt={row.createdAt} application={application} observationIDs={row.observationIDs} />
}

function MessageView({ base, message, status, createdAt, application, observationIDs }: { base: string; message: Message; status: string; createdAt?: string; application?: string; observationIDs?: string[] }) {
  const text = message.blocks.filter(block => block.type === 'text').map(block => block.text ?? '').join('\n\n')
  if (message.kind === 'compact') return <details className="management-tool-row"><summary>上下文摘要 · 原始对话已保留</summary><pre>{text}</pre><CopyMessage text={text} /></details>
  if (message.kind === 'system_notice') return observationIDs?.length ? <>{observationIDs.map(id => <ObservationMessage key={id} base={base} id={id} fallback={text} />)}</> : <details className="management-tool-row"><summary>{application ? '应用任务' : message.blocks.some(block => block.text?.startsWith('Explicit collaboration message')) ? '协作消息与结果' : '系统动态'}{status && ` · ${status}`}</summary>{message.blocks.some(block => block.text?.startsWith('External observation') || block.text?.startsWith('Execution environment updates')) && <p>历史来源未映射到可读取的事件，以下保留原始消息。</p>}{message.blocks.map((block, index) => block.type === 'image' ? <MessageMedia key={index} base={base} media={block.media} /> : <pre key={index}>{block.text}</pre>)}</details>
  const user = message.role === 'user'
  const date = createdAt ? new Date(createdAt) : null
  return <article className={`management-message ${user ? 'from-user' : 'from-agent'}`}>
    <div className="management-message-author"><span>{user ? '你' : message.model || 'Agent'}</span>{date && !Number.isNaN(date.valueOf()) && <time dateTime={createdAt} title={date.toLocaleString()}>{date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>}{text && <CopyMessage text={text} />}</div>
    {message.blocks.map((block, index) => block.type === 'text' ? user ? <p key={index} className="management-user-text">{block.text}</p> : <MessageMarkdown key={index} base={base} animating={status.startsWith('正在输出')}>{block.text ?? ''}</MessageMarkdown> : block.type === 'image' ? <MessageMedia key={`${index}:${block.media?.artifact_id}:${block.media?.sha256}`} base={base} media={block.media} /> : null)}
    {status && <small className="management-message-status">{status}</small>}
    {user && <InputCheckMarker message={message.id} />}
  </article>
}
