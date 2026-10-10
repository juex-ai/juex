import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { Activity, RotateCcw, ArrowDown, ArrowUp, FolderOpen, GitBranch, ImagePlus, LoaderCircle, Minimize2, Square, Settings2 } from 'lucide-react'
import { nanoid } from 'nanoid'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from '@/components/ui/sheet'
import { APIError, api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import { projectTranscript } from './timeline'
import { useTimeline } from './use-timeline'
import { InputChecks } from './input-checks'
import { ArtifactDialog } from './artifacts'
import { TranscriptExpansion, TranscriptItem } from './transcript'
import { projectActivity } from './activity'
import { useConversationDraft } from './drafts'
import { ThreadInspectionDialog } from './thread-inspection'
import { ThreadExplorer, threadDepth, useThreadBatch } from './thread-explorer'
import { ComposerImages } from './composer-images'
import { addDraftImages, imageAccept, removeDraftImage, uploadDraftImage } from './image-input'
import type { AgentDetail, CompactionRequest, InputReceipt, TenantAccess, Thread, User, WorkerRequest } from './schema'

const stateText: Record<string, string> = { idle: '就绪', queued: '排队中', running: '处理中', waiting: '等待执行结果', failed: '本轮失败', blocked: '等待处理' }

export function ConversationPage({ tenant, user }: { tenant: TenantAccess; user: User }) {
  const { agentId } = useParams()
  const base = `/tenants/${tenant.id}/agents/${agentId}`
  const [threadBatch, setThreadBatch] = useThreadBatch(user.id,base)
  const [search, setSearch] = useSearchParams()
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  const detail = useResource<AgentDetail>(base, revision)
  const threads = useResource<Thread[]>(`${base}/threads`, revision)
  const [workerName, setWorkerName] = useState<string | null>(null)
  const [workerRequest, setWorkerRequest] = useState<{ parent: string; body: WorkerRequest } | null>(null)
  const [filesOpen, setFilesOpen] = useState(false)
  const [threadsOpen, setThreadsOpen] = useState(false)
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
    try { const worker = await api<Thread>(`${base}/threads/${request.parent}/workers`, request.body); setWorkerName(null); setWorkerRequest(null); refresh(); setSearch({ thread: worker.id }); setThreadsOpen(false) } catch (err) { setError(errorText(err)); if (err instanceof APIError && err.status >= 400 && err.status < 500) setWorkerRequest(null) } finally { setBusy(false) }
  }
  if (detail.error || threads.error) return <Failure message={detail.error ?? threads.error!} retry={refresh} />
  if (!detail.data || !threads.data) return <Loading />
  const value = detail.data
  const canCreate = thread && !thread.application && thread.retention === 'active' && threadDepth(thread, threads.data) < value.agent.worker_depth

  return <>
    <PageHeading title={value.agent.name} description={value.owner_id !== user.id ? `正在代管此 Agent · 操作者：${user.email}` : `${thread?.name ?? '对话'} · ${stateText[liveThread?.id === thread?.id ? liveThread!.state : thread?.state ?? 'idle'] ?? ''}`} actions={<><Button variant="outline" asChild><Link to={`/t/${tenant.id}/agents/${agentId}/runtime`}><Activity />运行状态</Link></Button><Button variant="outline" asChild><Link to={`/t/${tenant.id}/agents/${agentId}/settings`}><Settings2 />配置</Link></Button><Button variant="outline" onClick={() => setFilesOpen(true)}><FolderOpen />文件与产物</Button><Button variant="outline" onClick={() => setThreadsOpen(true)}><GitBranch />Threads</Button></>} />
    {filesOpen && <ArtifactDialog key={base} base={base} agent={value.agent.id} thread={thread?.id} writable={value.can_execute} close={() => setFilesOpen(false)} />}
    {!value.can_execute && <Notice>此 Agent 或所属成员已停用，当前仅可查看历史。</Notice>}
    <Sheet open={threadsOpen} onOpenChange={open => { if (!busy) setThreadsOpen(open) }}><SheetContent className="management-threads-sheet"><SheetHeader><SheetTitle>Threads</SheetTitle><SheetDescription>Main 和 Workers 分别保留独立上下文；归档后历史仍可查看。</SheetDescription></SheetHeader>
      <ThreadExplorer unconfirmed={threadBatch} setUnconfirmed={setThreadBatch} base={base} threads={threads.data.map(item => liveThread?.id === item.id && liveThread.sequence >= item.sequence ? liveThread : item)} current={thread?.id} writable={value.can_execute} busy={busy} setBusy={setBusy} canCreate={!!canCreate || !!workerRequest} create={() => { setError(''); setWorkerName('') }} select={(item,keepOpen) => { setSearch(item.kind === 'main' ? {} : { thread: item.id }); if(!keepOpen)setThreadsOpen(false) }} refresh={refresh} />
      {error && workerName === null && <Notice error>{error}</Notice>}
    </SheetContent></Sheet>
    <div className="management-conversation-layout">

      {thread ? <ThreadConversation key={`${base}:${thread.id}`} base={base} thread={thread} actor={user.id} writable={value.can_execute && thread.retention === 'active'} onThread={setLiveThread} /> : <Failure message="此对话不存在或已不可访问。" retry={() => setSearch({})} />}
    </div>
    <Dialog open={workerName !== null} onOpenChange={open => { if (!open) setWorkerName(null) }}><DialogContent><DialogHeader><DialogTitle>创建 Worker</DialogTitle><DialogDescription>创建独立的对话上下文，可以与当前对话同时执行。</DialogDescription></DialogHeader><form onSubmit={createWorker}>{error && <Notice error>{error}</Notice>}{workerRequest && !busy && <Notice>创建结果尚未确认；重试会继续查询同一个 Worker，不会重复创建。</Notice>}<Field label="名称"><Input required maxLength={100} readOnly={workerRequest !== null} value={workerRequest?.body.name ?? workerName ?? ''} onChange={event => setWorkerName(event.target.value)} /></Field><DialogFooter><Button type="button" variant="outline" onClick={() => setWorkerName(null)}>关闭</Button><Button disabled={busy || !(workerRequest?.body.name ?? workerName)?.trim()}>{workerRequest ? '重试' : '创建'}</Button></DialogFooter></form></DialogContent></Dialog>
  </>
}

function storedReset(key: string): { request_id: string } | null {
  try { const id = sessionStorage.getItem(key); return id ? { request_id: id } : null } catch { return null }
}

function saveReset(key: string, request: { request_id: string } | null) {
  try { if (request) sessionStorage.setItem(key, request.request_id); else sessionStorage.removeItem(key) } catch { /* Keep the in-memory identity if browser storage is unavailable. */ }
}

function ThreadConversation({ base, thread, actor, writable, onThread }: { base: string; thread: Thread; actor: string; writable: boolean; onThread: (thread: Thread) => void }) {
  const storageKey = `${actor}:${base}:${thread.id}`
  const resetKey = `juex.reset:${actor}:${base}:${thread.id}`
  const { draft, store } = useConversationDraft(storageKey)
  const submission = draft.pending?.request
  const [compactFocus, setCompactFocus] = useState<string | null>(null)
  const [compactRequest, setCompactRequest] = useState<CompactionRequest | null>(null)
  const [resetOpen, setResetOpen] = useState(false)
  const [inspectionOpen, setInspectionOpen] = useState(false)
  const [resetRequest, setResetRequest] = useState<{ request_id: string } | null>(() => storedReset(resetKey))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const timeline = useTimeline(base, thread.id, revision)
  const scroll = useRef<HTMLDivElement>(null)
  const transcriptBody = useRef<HTMLDivElement>(null)
  const historyAnchor = useRef<{ keys: string[]; offset: number; sequence: number } | null>(null)
  const imagePicker = useRef<HTMLInputElement>(null)
  const nearBottom = useRef(true)
  const [atBottom, setAtBottom] = useState(true)
  const rows = projectTranscript(timeline.events, timeline.progress)
  const current = timeline.thread ?? thread
  const inputWritable = writable && !current.application
  const imagesReady = draft.images.every(image => image.state === 'ready' && image.media)
  const canSend = inputWritable && !busy && (submission || imagesReady && (draft.text.trim() || draft.images.length > 0))
  const running = current.pending_inputs > 0 || current.state === 'running' || current.state === 'waiting'
  useEffect(() => { if (timeline.thread) onThread(timeline.thread) }, [timeline.thread, onThread])
  useLayoutEffect(() => {
    const anchor = historyAnchor.current
    if (!anchor || anchor.sequence === timeline.previousSequence || !scroll.current || !transcriptBody.current) return
    const element = Array.from(transcriptBody.current.querySelectorAll<HTMLElement>('[data-transcript-row], [data-transcript-anchors]')).find(element => element.getClientRects().length > 0 && (element.dataset.transcriptAnchors ? JSON.parse(element.dataset.transcriptAnchors) as string[] : [element.dataset.transcriptRow!]).some(key => anchor.keys.includes(key)))
    if (element) scroll.current.scrollTop += element.getBoundingClientRect().top - scroll.current.getBoundingClientRect().top - anchor.offset
    historyAnchor.current = null
  }, [timeline.previousSequence])
  useEffect(() => { if (nearBottom.current && scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight }, [timeline.events])
  useEffect(() => {
    if (!transcriptBody.current) return
    const observer = new ResizeObserver(() => { if (nearBottom.current && scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight })
    observer.observe(transcriptBody.current)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    if (!submission) return
    const accepted = timeline.events.some(event => event.kind === 'input.accepted' && (event.data as { receipt?: InputReceipt }).receipt?.request_id === submission.request_id)
    if (accepted) { store.accept(storageKey, submission.request_id); setError('') }
  }, [timeline.events, submission, storageKey, store])
  async function send(event: FormEvent) {
    event.preventDefault(); if (!canSend) return
    const request = store.submit(storageKey, { request_id: nanoid(), thread_id: thread.id, text: draft.text, ...(draft.images.length ? { images: draft.images.map(image => image.media!) } : {}) })
    setBusy(true); setError('')
    try {
      await api<InputReceipt>(`${base}/inputs`, request)
      store.accept(storageKey, request.request_id)
    } catch (err) {
      setError(errorText(err))
      if (err instanceof APIError && err.status >= 400 && err.status < 500) store.reject(storageKey, request.request_id)
    } finally { setBusy(false) }
  }
  function addImages(files: File[]) {
    if (!inputWritable) return
    try { addDraftImages(store, storageKey, base, files); setError('') } catch (error) { setError(errorText(error)) }
  }
  function loadPrevious() {
    if (scroll.current && transcriptBody.current) {
      const top = scroll.current.getBoundingClientRect().top
      const element = Array.from(transcriptBody.current.querySelectorAll<HTMLElement>('[data-transcript-row], [data-transcript-anchors]')).find(element => element.getClientRects().length > 0 && element.getBoundingClientRect().bottom > top && (!element.dataset.transcriptRow || !element.querySelector('[data-transcript-anchors]')))
      if (element) historyAnchor.current = { keys: element.dataset.transcriptAnchors ? JSON.parse(element.dataset.transcriptAnchors) : [element.dataset.transcriptRow!], offset: element.getBoundingClientRect().top - top, sequence: timeline.previousSequence }
      nearBottom.current = false; setAtBottom(false)
    }
    void timeline.loadPrevious()
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
  async function resetContext(event: FormEvent) {
    event.preventDefault(); if (busy) return
    const request = resetRequest ?? { request_id: nanoid() }
    setBusy(true); setError(''); setResetRequest(request)
    saveReset(resetKey, request)
    try { const updated = await api<Thread>(`${base}/threads/${thread.id}/reset-context`, request); onThread(updated); setRevision(value => value + 1); setResetOpen(false); setResetRequest(null); saveReset(resetKey, null) } catch (err) {
      setError(errorText(err))
    } finally { setBusy(false) }
  }
  return <section className="management-conversation" aria-label={`${thread.name} 对话`}>
    {inspectionOpen && <ThreadInspectionDialog base={base} thread={thread.id} close={() => setInspectionOpen(false)} />}
    <div className="management-conversation-heading"><strong>{thread.name}</strong><Button size="sm" variant="ghost" onClick={() => setInspectionOpen(true)}>状态与上下文</Button><Button size="sm" variant="ghost" disabled={!inputWritable || busy} onClick={() => { setError(''); setCompactFocus(compactRequest?.focus ?? '') }}><Minimize2 size={14} />压缩上下文</Button><Button size="sm" variant="ghost" disabled={!inputWritable || busy || (!resetRequest && (running || current.held_inputs > 0 || !['idle', 'failed'].includes(current.state)))} onClick={() => { setError(''); setResetOpen(true) }}><RotateCcw size={14} />新上下文</Button><span>{running && <LoaderCircle size={13} className="animate-spin" />}{stateText[current.state] ?? current.state}</span></div>
    <div className="management-transcript-frame"><div className="management-transcript" ref={scroll} onScroll={event => { const element = event.currentTarget; nearBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 80; setAtBottom(nearBottom.current) }}><div ref={transcriptBody}>
      {timeline.hasPrevious && <Button variant="ghost" className="management-load-history" disabled={timeline.loadingPrevious} onClick={loadPrevious}>{timeline.loadingPrevious ? '正在加载历史…' : '加载更早的消息'}</Button>}
      {timeline.historyError && <Notice error>{timeline.historyError}；更早的历史未加载，可重试。</Notice>}
      <InputChecks base={base} thread={thread.id} events={timeline.events}><TranscriptExpansion>{!timeline.thread ? <Loading /> : rows.length === 0 ? <Empty title="从一条消息开始">说明你要完成的事，Agent 会在这里持续处理。</Empty> : projectActivity(rows, timeline.events).map(row => <div key={row.id} data-transcript-row={row.id}><TranscriptItem base={base} row={row} application={current.application} /></div>)}</TranscriptExpansion></InputChecks>
      {current.state === 'running' && <div className="management-working" role="status"><LoaderCircle size={14} className="animate-spin" />正在处理…</div>}
    </div></div>{!atBottom && <Button className="management-jump-bottom" size="sm" variant="outline" onClick={() => { nearBottom.current = true; setAtBottom(true); if (scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight }}><ArrowDown size={14} />跳到底部</Button>}</div>
    <div className="management-composer-wrap">{timeline.error && <Failure message={timeline.error} retry={() => setRevision(value => value + 1)} />}{error && <Notice error>{error}</Notice>}
      {(current.queued_inputs ?? 0) > 0 && <details className="management-queued-inputs"><summary>排队中 · {current.queued_inputs} 条输入</summary><p>按接收顺序等待，不包括正在执行的输入；下方只预览已加载窗口内的待执行消息。</p><ol>{rows.filter(row => row.kind === 'message' && row.status === '已接收，等待执行').map(row => row.kind === 'message' && <li key={row.id}>{row.message.blocks.map(block => block.text).join('\n')}</li>)}</ol></details>}
      {current.held_inputs > 0 && <Notice>有 {current.held_inputs} 条输入已暂停，不会自动重试。停止会放弃这些输入，并请求结束本对话的后台操作；历史保留。{!running && writable && <Button type="button" variant="outline" disabled={busy} onClick={() => void cancel()}>停止并放弃暂停输入</Button>}</Notice>}
      {submission && !busy && <Notice>发送结果尚未确认。重试会使用同一个请求编号，避免重复执行。</Notice>}
      <form className="management-composer" onSubmit={send} onDragOver={event => { if (event.dataTransfer.types.includes('Files')) event.preventDefault() }} onDrop={event => { if (event.dataTransfer.files.length) { event.preventDefault(); addImages(Array.from(event.dataTransfer.files)) } }}>
        {draft.images.length > 0 && <ComposerImages images={draft.images} base={base} remove={id => removeDraftImage(store, storageKey, id)} retry={id => void uploadDraftImage(store, storageKey, base, id)} />}
        <Textarea aria-label="消息" rows={3} maxLength={32000} value={draft.text} disabled={!inputWritable} onChange={event => store.edit(storageKey, event.target.value)} onPaste={event => { const files = Array.from(event.clipboardData.files); if (files.length) { event.preventDefault(); addImages(files) } }} placeholder={current.application ? '此 Worker 执行应用审核，可查看进度或停止' : writable ? '告诉 Agent 你想完成什么…' : '当前只能查看历史'} onKeyDown={event => { if (event.key === 'Backspace' && !event.nativeEvent.isComposing && !draft.text && draft.images.length) { event.preventDefault(); removeDraftImage(store, storageKey, draft.images.at(-1)!.id) } if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} />
        <input ref={imagePicker} type="file" accept={imageAccept} multiple hidden aria-label="选择图片" onChange={event => { addImages(Array.from(event.target.files ?? [])); event.target.value = '' }} />
        <div className="management-composer-actions"><span><Button type="button" size="icon" variant="ghost" aria-label="添加图片" title="选择、粘贴或拖入图片，最多 8 张，每张 10 MiB" disabled={!inputWritable || draft.images.length >= 8} onClick={() => imagePicker.current?.click()}><ImagePlus size={18} /></Button><small>Enter 发送 · Shift + Enter 换行</small></span><span>{running && writable && <Button type="button" variant="outline" disabled={busy} onClick={() => void cancel()}><Square size={13} />停止</Button>}<Button type="submit" disabled={!canSend} aria-label={submission ? '重试发送' : '发送消息'}>{busy ? <LoaderCircle className="animate-spin" /> : <ArrowUp />}{submission ? '重试' : running ? '排队' : '发送'}</Button></span></div>
      </form>
    </div>
    <Dialog open={compactFocus !== null} onOpenChange={open => { if (!open && !busy) setCompactFocus(null) }}><DialogContent><DialogHeader><DialogTitle>压缩上下文</DialogTitle><DialogDescription>为后续任务整理摘要，完整对话历史仍会保留。当前任务正在运行时，压缩会按提交顺序等待执行。</DialogDescription></DialogHeader><form onSubmit={compact}>{error && <Notice error>{error}</Notice>}{compactRequest && <Notice>结果尚未确认，重试会使用同一个请求编号。</Notice>}<Field label="需要重点保留的内容（可选）"><Textarea value={compactFocus ?? ''} maxLength={1000} readOnly={compactRequest !== null} onChange={event => setCompactFocus(event.target.value)} placeholder="例如：关键决策、文件路径和未完成事项" /></Field><DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={() => setCompactFocus(null)}>关闭</Button><Button disabled={busy}>{busy ? '提交中…' : compactRequest ? '重试' : '开始压缩'}</Button></DialogFooter></form></DialogContent></Dialog>
    <Dialog open={resetOpen} onOpenChange={open => { if (!busy) setResetOpen(open) }}><DialogContent><DialogHeader><DialogTitle>开始新上下文</DialogTitle><DialogDescription>后续消息不再携带当前对话内容。启用的 Notes 会清空，已完成的 Tasks 会移出当前列表；未完成 Tasks、完整历史和工作文件保留。</DialogDescription></DialogHeader><form onSubmit={resetContext}>{error && <Notice error>{error}</Notice>}{resetRequest && <Notice>结果尚未确认，重试会使用同一个请求编号。</Notice>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={() => setResetOpen(false)}>关闭</Button><Button disabled={busy}>{busy ? '提交中…' : resetRequest ? '重试' : '开始新上下文'}</Button></DialogFooter></form></DialogContent></Dialog>
  </section>
}
