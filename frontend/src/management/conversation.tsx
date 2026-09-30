import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { Archive, RotateCcw, ArrowLeft, ArrowUp, FolderOpen, GitBranch, LoaderCircle, Minimize2, Square } from 'lucide-react'
import { nanoid } from 'nanoid'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { MessageResponse } from '@/components/ai-elements/message'
import { APIError, api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import { projectTranscript } from './timeline'
import { ArtifactDialog } from './artifacts'
import type { AgentDetail, CompactionRequest, Event, InputReceipt, InputRequest, Message, TenantAccess, Thread, Timeline, User, WorkerRequest } from './schema'

const stateText: Record<string, string> = { idle: '就绪', queued: '排队中', running: '处理中', waiting: '等待工具结果', failed: '本轮失败', blocked: '等待处理' }

export function ConversationPage({ tenant, user }: { tenant: TenantAccess; user: User }) {
  const { agentId } = useParams()
  const base = `/tenants/${tenant.id}/agents/${agentId}`
  const [search, setSearch] = useSearchParams()
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  const detail = useResource<AgentDetail>(base, revision)
  const threads = useResource<Thread[]>(`${base}/threads`, revision)
  const [workerName, setWorkerName] = useState<string | null>(null)
  const [workerRequest, setWorkerRequest] = useState<{ parent: string; body: WorkerRequest } | null>(null)
  const [filesOpen, setFilesOpen] = useState(false)
  const [showArchived, setShowArchived] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [liveThread, setLiveThread] = useState<Thread | null>(null)
  const thread = threads.data?.find(value => search.has('thread') ? value.id === search.get('thread') : value.kind === 'main')
  useEffect(() => { const timer = window.setInterval(refresh, 10_000); return () => window.clearInterval(timer) }, [])
  async function createWorker(event: FormEvent) {
    event.preventDefault(); if (!thread) return
    const request = workerRequest ?? { parent: thread.id, body: { request_id: nanoid(), name: workerName ?? '' } }
    setWorkerRequest(request)
    setBusy(true); setError('')
    try { const worker = await api<Thread>(`${base}/threads/${request.parent}/workers`, request.body); setWorkerName(null); setWorkerRequest(null); refresh(); setSearch({ thread: worker.id }) } catch (err) { setError(errorText(err)); if (err instanceof APIError && err.status >= 400 && err.status < 500) setWorkerRequest(null) } finally { setBusy(false) }
  }
  async function archiveWorker(archived: boolean) {
    if (!thread) return
    setBusy(true); setError('')
    try { await api<Thread>(`${base}/threads/${thread.id}/archive`, { archived }); refresh(); setLiveThread(null); setShowArchived(archived) } catch (err) { setError(err instanceof APIError && err.status === 409 ? archived ? "请先处理此 Worker 未完成的任务、执行操作、结果通知和子 Worker，再归档。" : "请先恢复父 Worker。" : errorText(err)) } finally { setBusy(false) }
  }
  if (detail.error || threads.error) return <Failure message={detail.error ?? threads.error!} retry={refresh} />
  if (!detail.data || !threads.data) return <Loading />
  const value = detail.data
  const depth = (item: Thread): number => item.parent_id ? 1 + depth(threads.data!.find(parent => parent.id === item.parent_id)!) : 0
  const canCreate = thread && !thread.application && thread.retention === 'active' && depth(thread) < value.agent.worker_depth
  const back = `/t/${tenant.id}/${value.owner_id === user.id ? 'fleet' : `users/${value.owner_id}`}`
  return <>
    <PageHeading title={value.agent.name} description={value.owner_id !== user.id ? `正在代管此 Agent · 操作者：${user.email}` : 'Main 和 Workers 分别保存对话上下文。'} actions={<><Button variant="outline" onClick={() => setFilesOpen(true)}><FolderOpen />文件与产物</Button><Button variant="outline" asChild><Link to={back}><ArrowLeft />返回 Fleet</Link></Button></>} />
    {filesOpen && <ArtifactDialog key={base} base={base} agent={value.agent.id} writable={value.can_execute} close={() => setFilesOpen(false)} />}
    {!value.can_execute && <Notice>此 Agent 或所属成员已停用，当前仅可查看历史。</Notice>}
    <div className="management-conversation-layout">
      <aside className="management-thread-list"><div className="management-thread-heading"><strong>对话</strong><Button variant="ghost" size="icon" aria-label="创建 Worker" disabled={(!canCreate && !workerRequest) || !value.can_execute} onClick={() => { setError(''); setWorkerName('') }}><GitBranch size={16} /></Button></div><nav aria-label="Agent 对话">{threads.data.filter(item => item.kind === 'main' || (showArchived ? item.retention === 'archived' : item.retention === 'active')).map(item => { const current = liveThread?.id === item.id && liveThread.sequence >= item.sequence ? liveThread : item; return <button key={item.id} className={item.id === thread?.id ? 'active' : ''} onClick={() => setSearch(item.kind === 'main' ? {} : { thread: item.id })}><span>{item.name}</span><small>{item.application ? `${item.application} Worker` : item.kind === 'main' ? 'Main' : 'Worker'} · {current.retention === 'archived' ? '已归档' : stateText[current.state] ?? current.state}</small></button> })}</nav><label className="management-checkbox"><input type="checkbox" checked={showArchived} onChange={event => setShowArchived(event.target.checked)} />查看已归档 Workers</label>{thread?.kind === 'worker' && value.can_execute && <Button variant="ghost" disabled={busy} onClick={() => void archiveWorker(thread.retention !== 'archived')}>{thread.retention === 'archived' ? <><RotateCcw size={14} />恢复 Worker</> : <><Archive size={14} />归档 Worker</>}</Button>}{error && workerName === null && <Notice error>{error}</Notice>}</aside>
      {thread ? <ThreadConversation key={`${base}:${thread.id}`} base={base} thread={thread} actor={user.id} writable={value.can_execute && thread.retention === 'active'} onThread={setLiveThread} /> : <Failure message="此对话不存在或已不可访问。" retry={() => setSearch({})} />}
    </div>
    <Dialog open={workerName !== null} onOpenChange={open => { if (!open) setWorkerName(null) }}><DialogContent><DialogHeader><DialogTitle>创建 Worker</DialogTitle><DialogDescription>创建独立的对话上下文，可以与当前对话同时执行。</DialogDescription></DialogHeader><form onSubmit={createWorker}>{error && <Notice error>{error}</Notice>}{workerRequest && !busy && <Notice>创建结果尚未确认；重试会继续查询同一个 Worker，不会重复创建。</Notice>}<Field label="名称"><Input required maxLength={100} readOnly={workerRequest !== null} value={workerRequest?.body.name ?? workerName ?? ''} onChange={event => setWorkerName(event.target.value)} /></Field><DialogFooter><Button type="button" variant="outline" onClick={() => setWorkerName(null)}>关闭</Button><Button disabled={busy || !(workerRequest?.body.name ?? workerName)?.trim()}>{workerRequest ? '重试' : '创建'}</Button></DialogFooter></form></DialogContent></Dialog>
  </>
}

function useTimeline(base: string, thread: string, revision: number) {
  const [state, setState] = useState<{ events: Event[]; thread?: Thread; error?: string }>({ events: [] })
  useEffect(() => {
    const controller = new AbortController()
    let cursor = 0
    let events: Event[] = []
    let timer: number | undefined
    let failures = 0
    const poll = async () => {
      try {
        const value = await api<Timeline>(`${base}/threads/${thread}/events?after=${cursor}&limit=200`, undefined, undefined, controller.signal)
        if (controller.signal.aborted) return
        cursor = value.next_sequence; events = [...events, ...value.events]
        failures = 0
        setState({ events, thread: value.thread })
        timer = window.setTimeout(() => void poll(), value.has_more ? 0 : 750)
      } catch (error) {
        if (controller.signal.aborted) return
        setState(previous => ({ ...previous, error: errorText(error) }))
        if (!(error instanceof APIError) || error.status >= 500 || error.status === 429) {
          failures += 1
          timer = window.setTimeout(() => void poll(), Math.min(15_000, 1000 * 2 ** Math.min(failures, 4)))
        }
      }
    }
    void poll()
    return () => { controller.abort(); window.clearTimeout(timer) }
  }, [base, thread, revision])
  return state
}

function storedSubmission(key: string): InputRequest | null {
  try { const raw = sessionStorage.getItem(key); if (!raw) return null; const value = JSON.parse(raw) as InputRequest; return typeof value.request_id === 'string' && typeof value.text === 'string' ? value : null } catch { return null }
}

function saveSubmission(key: string, request: InputRequest | null) {
  try { if (request) sessionStorage.setItem(key, JSON.stringify(request)); else sessionStorage.removeItem(key) } catch { /* The in-memory request ID still deduplicates retries when storage is unavailable. */ }
}

function ThreadConversation({ base, thread, actor, writable, onThread }: { base: string; thread: Thread; actor: string; writable: boolean; onThread: (thread: Thread) => void }) {
  const storageKey = `juex.pending:${actor}:${base}:${thread.id}`
  const [submission, setSubmission] = useState<InputRequest | null>(() => storedSubmission(storageKey))
  const [compactFocus, setCompactFocus] = useState<string | null>(null)
  const [compactRequest, setCompactRequest] = useState<CompactionRequest | null>(null)
  const [draft, setDraft] = useState(() => storedSubmission(storageKey)?.text ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const timeline = useTimeline(base, thread.id, revision)
  const scroll = useRef<HTMLDivElement>(null)
  const nearBottom = useRef(true)
  const rows = projectTranscript(timeline.events)
  const current = timeline.thread ?? thread
  const inputWritable = writable && !current.application
  const running = current.pending_inputs > 0 || current.state === 'running' || current.state === 'waiting'
  useEffect(() => { if (timeline.thread) onThread(timeline.thread) }, [timeline.thread, onThread])
  useEffect(() => { if (nearBottom.current && scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight }, [timeline.events])
  useEffect(() => {
    if (!submission) return
    const accepted = timeline.events.some(event => event.kind === 'input.accepted' && (event.data as { receipt?: InputReceipt }).receipt?.request_id === submission.request_id)
    if (accepted) { setSubmission(null); setDraft(''); setError(''); saveSubmission(storageKey, null) }
  }, [timeline.events, submission, storageKey])
  async function send(event: FormEvent) {
    event.preventDefault(); if (!inputWritable || !draft.trim() || busy) return
    const request = submission ?? { request_id: nanoid(), thread_id: thread.id, text: draft }
    setBusy(true); setError(''); setSubmission(request)
    try {
      saveSubmission(storageKey, request)
      await api<InputReceipt>(`${base}/inputs`, request)
      saveSubmission(storageKey, null); setSubmission(null); setDraft('')
    } catch (err) {
      setError(errorText(err))
      if (err instanceof APIError && err.status >= 400 && err.status < 500) { setSubmission(null); saveSubmission(storageKey, null) }
    } finally { setBusy(false) }
  }
  async function compact(event: FormEvent) {
    event.preventDefault(); if (busy) return
    const request = compactRequest ?? { request_id: nanoid(), focus: compactFocus ?? '' }
    setBusy(true); setError(''); setCompactRequest(request)
    try { await api<InputReceipt>(`${base}/threads/${thread.id}/compact`, request); setCompactFocus(null); setCompactRequest(null) } catch (err) {
      setError(errorText(err))
      if (err instanceof APIError && err.status >= 400 && err.status < 500) setCompactRequest(null)
    } finally { setBusy(false) }
  }
  async function cancel() {
    setError(''); setBusy(true)
    try { await api(`${base}/threads/${thread.id}/cancel`, {}) } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  return <section className="management-conversation" aria-label={`${thread.name} 对话`}>
    <div className="management-conversation-heading"><strong>{thread.name}</strong><Button size="sm" variant="ghost" disabled={!inputWritable || busy} onClick={() => { setError(''); setCompactFocus(compactRequest?.focus ?? '') }}><Minimize2 size={14} />压缩上下文</Button><span>{running && <LoaderCircle size={13} className="animate-spin" />}{stateText[current.state] ?? current.state}</span></div>
    <div className="management-transcript" ref={scroll} onScroll={event => { const element = event.currentTarget; nearBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 80 }}>
      {!timeline.thread ? <Loading /> : rows.length === 0 ? <Empty title="从一条消息开始">说明你要完成的事，Agent 会在这里持续处理。</Empty> : rows.map(row => row.kind === 'notice' ? <p className="management-turn-notice" key={row.id}>{row.text}</p> : <MessageView key={row.id} message={row.message} status={row.status} />)}
      {current.state === 'running' && <div className="management-working" role="status"><LoaderCircle size={14} className="animate-spin" />正在处理…</div>}
    </div>
    <div className="management-composer-wrap">{timeline.error && <Failure message={timeline.error} retry={() => setRevision(value => value + 1)} />}{error && <Notice error>{error}</Notice>}
      {submission && !busy && <Notice>发送结果尚未确认。重试会使用同一个请求编号，避免重复执行。</Notice>}
      <form className="management-composer" onSubmit={send}><Textarea aria-label="消息" rows={3} maxLength={32000} value={draft} readOnly={submission !== null} disabled={!inputWritable} onChange={event => setDraft(event.target.value)} placeholder={current.application ? '此 Worker 执行应用审核，可查看进度或停止' : writable ? '告诉 Agent 你想完成什么…' : '当前只能查看历史'} onKeyDown={event => { if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} /><div><small>Enter 发送 · Shift + Enter 换行</small><span>{running && writable && <Button type="button" variant="outline" disabled={busy} onClick={() => void cancel()}><Square size={13} />停止</Button>}<Button type="submit" disabled={!inputWritable || busy || !draft.trim()} aria-label={submission ? '重试发送' : '发送消息'}>{busy ? <LoaderCircle className="animate-spin" /> : <ArrowUp />}{submission ? '重试' : '发送'}</Button></span></div></form>
    </div>
    <Dialog open={compactFocus !== null} onOpenChange={open => { if (!open && !busy) setCompactFocus(null) }}><DialogContent><DialogHeader><DialogTitle>压缩上下文</DialogTitle><DialogDescription>为后续任务整理摘要，完整对话历史仍会保留。当前任务正在运行时，压缩会按提交顺序等待执行。</DialogDescription></DialogHeader><form onSubmit={compact}>{error && <Notice error>{error}</Notice>}{compactRequest && <Notice>结果尚未确认，重试会使用同一个请求编号。</Notice>}<Field label="需要重点保留的内容（可选）"><Textarea value={compactFocus ?? ''} maxLength={1000} readOnly={compactRequest !== null} onChange={event => setCompactFocus(event.target.value)} placeholder="例如：关键决策、文件路径和未完成事项" /></Field><DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={() => setCompactFocus(null)}>关闭</Button><Button disabled={busy}>{busy ? '提交中…' : compactRequest ? '重试' : '开始压缩'}</Button></DialogFooter></form></DialogContent></Dialog>
  </section>
}

function MessageView({ message, status }: { message: Message; status: string }) {
  if (message.kind === 'tool_result') return <div className="management-message from-agent">{message.blocks.map((block, index) => <details key={index} className="management-tool-row"><summary>{block.is_error ? '执行未完成' : '执行结果'} · {block.tool_name}</summary><pre>{block.content}</pre></details>)}</div>
  if (message.kind === 'compact') return <details className="management-tool-row"><summary>上下文摘要 · 原始对话已保留</summary>{message.blocks.map((block, index) => <pre key={index}>{block.text}</pre>)}</details>
  if (message.kind === 'system_notice') return <details className="management-tool-row"><summary>{message.blocks.some(block => block.text?.startsWith('Explicit collaboration message')) ? '协作消息与结果' : '执行环境动态'}</summary>{message.blocks.map((block, index) => <pre key={index}>{block.text}</pre>)}</details>
  const user = message.role === 'user'
  return <article className={`management-message ${user ? 'from-user' : 'from-agent'}`}><div className="management-message-author">{user ? '你' : 'Agent'}</div>{message.blocks.map((block, index) => {
    if (block.type === 'text') return user ? <p key={index} className="management-user-text">{block.text}</p> : <MessageResponse key={index} isAnimating={false}>{block.text ?? ''}</MessageResponse>
    if (block.type === 'reasoning') return <details key={index} className="management-tool-row"><summary>思考过程</summary><p>{block.text || '此部分未提供可显示的内容。'}</p></details>
    if (block.type === 'tool_use') return <details key={index} className="management-tool-row"><summary>调用 {block.tool_name}</summary><pre>{JSON.stringify(block.input, null, 2)}</pre></details>
    if (block.type === 'tool_result') return <details key={index} className="management-tool-row"><summary>{block.is_error ? '执行失败' : '执行结果'} · {block.tool_name}</summary><pre>{block.content}</pre></details>
    return null
  })}{status && <small className="management-message-status">{status}</small>}</article>
}
