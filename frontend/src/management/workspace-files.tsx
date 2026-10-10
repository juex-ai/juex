import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowLeft, Copy, Download, File, Folder, RefreshCw, Search, WrapText } from 'lucide-react'
import { nanoid } from 'nanoid'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { writeClipboardText } from '@/lib/clipboard'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice } from './components'
import { FilePreviewText } from './file-preview-text'
import { downloadMedia } from './media-download'
import { useResource } from './use-resource'
import type { Artifact, Environment, ThreadInspection, Transfer, TransferRequest, WorkspaceEntry, WorkspaceQuery, WorkspaceReadReceipt, WorkspaceReadRequest } from './schema'

const pause = (signal: AbortSignal) => new Promise<void>((resolve, reject) => {
  signal.throwIfAborted()
  const abort = () => { window.clearTimeout(timer); reject(signal.reason) }
  const timer = window.setTimeout(() => { signal.removeEventListener('abort', abort); resolve() }, 500)
  signal.addEventListener('abort', abort, { once: true })
})

export function useWorkspaceRead(base: string, request: WorkspaceReadRequest | null, revision = 0) {
  const [state, setState] = useState<{ id: string; receipt?: WorkspaceReadReceipt; error?: string }>({ id: '' })
  useEffect(() => {
    if (!request) return
    const controller = new AbortController()
    const run = async () => {
      try {
        let receipt = await api<WorkspaceReadReceipt>(`${base}/workspace-reads`, request, undefined, controller.signal)
        for (let i = 0; ['waiting', 'dispatched', 'accepted', 'running'].includes(receipt.state); i++) {
          if (i >= 120) throw new Error('环境尚未返回文件信息。重试会查询同一次读取。')
          setState({ id: request.request_id, receipt })
          await pause(controller.signal)
          receipt = await api<WorkspaceReadReceipt>(`${base}/workspace-reads/${receipt.environment_id}/${encodeURIComponent(receipt.operation_id)}`, undefined, undefined, controller.signal)
        }
        if (receipt.state !== 'completed' || !receipt.listing) throw new Error(receipt.error || `文件读取未完成：${receipt.state}`)
        setState({ id: request.request_id, receipt })
      } catch (error) { if (!controller.signal.aborted) setState({ id: request.request_id, error: errorText(error) }) }
    }
    void run()
    return () => controller.abort()
  }, [base, request, revision])
  return state.id === request?.request_id ? state : { id: request?.request_id ?? '' }
}

export function WorkspaceFiles({ base, thread, working = false }: { base: string; thread?: string; working?: boolean }) {
  const [revision, setRevision] = useState(0)
  const [selected, setSelected] = useState('')
  const refreshEnvironment = useCallback(() => setRevision(value => value + 1), [])
  const environments = useResource<Environment[]>(`${base}/environments`, revision)
  if (environments.error) return <Failure message={environments.error} retry={() => setRevision(value => value + 1)} />
  if (!environments.data) return <Loading />
  const available = environments.data.filter(environment => environment.capabilities.includes('files'))
  if (working) return thread ? <WorkingFiles base={base} thread={thread} environments={available} refreshEnvironment={refreshEnvironment} /> : <Notice>从具体对话打开文件，可查看该 Thread 的工作文件。</Notice>
  const environment = available.find(environment => environment.id === selected || !selected && environment.default)
  return <section aria-label="Workspace 文件">
    <Field label="文件所在环境"><select value={environment?.id ?? ''} onChange={event => setSelected(event.target.value)}><option value="">请选择文件环境</option>{available.map(environment => <option key={environment.id} value={environment.id}>{environment.name} · {environment.online ? '在线' : '离线'}</option>)}</select></Field>
    {environment ? <WorkspaceBrowser key={`${environment.id}:${environment.working_directory}`} base={base} environment={environment} directory={environment.working_directory} refreshEnvironment={refreshEnvironment} /> : <Empty title="尚未选择文件环境">请在 Agent 配置中设置默认环境，或在上方选择已授权的环境。</Empty>}
  </section>
}

function WorkingFiles({ base, thread, environments, refreshEnvironment }: { base: string; thread: string; environments: Environment[]; refreshEnvironment: () => void }) {
  const [revision, setRevision] = useState(0)
  const inspection = useResource<ThreadInspection>(`${base}/threads/${thread}/inspection`, revision)
  if (inspection.error) return <Failure message={inspection.error} retry={() => setRevision(value => value + 1)} />
  if (!inspection.data) return <Loading />
  const location = inspection.data.working_files
  if (!location) return <Empty title="此对话尚未创建工作文件">Agent 开始使用工作文件后，会在这里显示其原始环境与目录。</Empty>
  const environment = environments.find(environment => environment.id === location.environment_id)
  if (!environment) return <Notice>工作文件位于当前不可访问的环境 {location.environment_id}：{location.directory}。请恢复原环境授权后查看。</Notice>
  return <WorkspaceBrowser key={`${environment.id}:${location.directory}`} base={base} environment={environment} directory={location.directory} refreshEnvironment={refreshEnvironment} />
}

function WorkspaceBrowser({ base, environment, directory, refreshEnvironment }: { base: string; environment: Environment; directory: string; refreshEnvironment: () => void }) {
  const makeRequest = (query: WorkspaceQuery): WorkspaceReadRequest => ({ request_id: nanoid(), environment_id: environment.id, directory, query })
  const [request, setRequest] = useState(() => makeRequest({ path: '.', hidden: false, read: false }))
  const [preview, setPreview] = useState<WorkspaceReadRequest | null>(null)
  const [search, setSearch] = useState('')
  const [revision, setRevision] = useState(0)
  const [notice, setNotice] = useState('')
  const result = useWorkspaceRead(base, request, revision)
  useEffect(() => { if (result.receipt?.state === 'completed') refreshEnvironment() }, [result.receipt?.state, result.receipt?.operation_id, refreshEnvironment])
  const query = request.query
  const listing = result.receipt?.listing
  const fullPath = (name: string) => `${directory.replace(/\/$/, '')}${name === '.' ? '' : `/${name}`}`
  function browse(change: Partial<WorkspaceQuery>) { setRequest(makeRequest({ ...query, after: undefined, ...change })); setPreview(null); setNotice('') }
  function find(event: FormEvent) { event.preventDefault(); browse({ search: search.trim() }) }
  async function copy(value: string) { try { await writeClipboardText(value); setNotice('已复制。') } catch (error) { setNotice(errorText(error)) } }
  return <div className="management-workspace-browser">
    <p className="management-file-location"><strong>{environment.name}</strong> · {environment.kind === 'hosted' ? '托管环境' : '本机 Shell 环境'} · {environment.online ? '在线' : '离线'}<code>{directory}</code></p>
    {!environment.online && <Notice>此环境当前离线。读取会等待原环境上线，不会切换到其它环境。</Notice>}
    <form className="management-workspace-search" onSubmit={find}><Input aria-label="搜索文件名" value={search} onChange={event => setSearch(event.target.value)} placeholder="在当前目录及子目录搜索文件名" /><Button variant="outline" type="submit"><Search size={15} />搜索</Button></form>
    <div className="management-row-actions"><Button variant="ghost" size="sm" disabled={query.path === '.'} onClick={() => browse({ path: query.path.includes('/') ? query.path.slice(0, query.path.lastIndexOf('/')) : '.', search: undefined })}><ArrowLeft size={14} />上一级</Button><Button variant="ghost" size="sm" onClick={() => browse({})}><RefreshCw size={14} />刷新文件</Button><label className="management-checkbox"><input type="checkbox" checked={query.hidden} onChange={event => browse({ hidden: event.target.checked })} />显示隐藏文件</label></div>
    <p className="management-file-location"><code>{fullPath(query.path)}</code><Button variant="ghost" size="icon" aria-label="复制目录路径" onClick={() => void copy(fullPath(query.path))}><Copy size={14} /></Button></p>
    {notice && <p role="status">{notice}</p>}
    {result.error ? <Failure message={result.error} retry={() => setRevision(value => value + 1)} /> : !listing ? <Loading /> : <>
      {listing.entries.length === 0 ? <Empty title={query.search ? '没有找到匹配文件' : '目录为空'}>可刷新、显示隐藏文件，或检查所选环境与路径。</Empty> : <ul className="management-workspace-list" aria-label="工作目录文件">{listing.entries.map(entry => <li key={entry.path}><Button variant="ghost" disabled={!['directory', 'file'].includes(entry.kind)} onClick={() => entry.kind === 'directory' ? browse({ path: entry.path, search: undefined }) : setPreview(makeRequest({ path: entry.path, hidden: query.hidden, read: true }))}>{entry.kind === 'directory' ? <Folder size={16} /> : <File size={16} />}<span>{query.search ? entry.path : entry.name}</span></Button><small>{entry.kind === 'file' ? `${entry.size.toLocaleString()} B` : entry.kind === 'symlink' ? '符号链接' : entry.kind === 'directory' ? '目录' : '特殊文件'}</small><Button variant="ghost" size="icon" aria-label={`复制 ${entry.name} 路径`} onClick={() => void copy(fullPath(entry.path))}><Copy size={13} /></Button></li>)}</ul>}
      {listing.scan_limited && <Notice>目录内容较多，本次只检查了前 20,000 项。请进入更具体的目录后搜索。</Notice>}
      {listing.next_cursor && <Button variant="outline" onClick={() => browse({ after: listing.next_cursor })}>下一页文件</Button>}
      {query.after && <Button variant="ghost" onClick={() => browse({ after: undefined })}>回到第一页</Button>}
    </>}
    {preview && <WorkspacePreview key={preview.request_id} base={base} request={preview} environment={environment} close={() => setPreview(null)} />}
  </div>
}

export function WorkspacePreview({ base, request, environment, close, onRead }: { base: string; request: WorkspaceReadRequest; environment: Environment; close: () => void; onRead?: () => void }) {
  const [revision, setRevision] = useState(0)
  const [copied, setCopied] = useState(false)
  const [wrap, setWrap] = useState(false)
  const [error, setError] = useState('')
  const result = useWorkspaceRead(base, request, revision)
  useEffect(() => { if (result.receipt?.state === 'completed') onRead?.() }, [result.receipt?.state, result.receipt?.operation_id, onRead])
  const preview = result.receipt?.listing?.preview
  return <section className="management-workspace-preview" aria-label="文件预览"><div className="management-panel-heading"><h3>{request.query.path}</h3><Button size="sm" variant="ghost" onClick={close}>关闭预览</Button></div>
    {result.error ? <Failure message={result.error} retry={() => setRevision(value => value + 1)} /> : !preview ? <Loading /> : <>
      <small>{preview.entry.size.toLocaleString()} B · {new Date(preview.entry.modified_at).toLocaleString()}</small>
      {!preview.binary && <><Button size="sm" variant="ghost" aria-pressed={wrap} onClick={() => setWrap(value => !value)}><WrapText size={14} />自动换行</Button><FilePreviewText code={preview.text} path={preview.entry.path} wrap={wrap} />{preview.truncated && <Notice>预览已截断（最多 256 KiB）。下载原文件可获取完整内容。</Notice>}<Button size="sm" variant="outline" onClick={() => void writeClipboardText(preview.text).then(() => setCopied(true)).catch(error => setError(errorText(error)))}><Copy size={14} />{copied ? '已复制内容' : '复制内容'}</Button></>}
      {error && <Notice error>{error}</Notice>}
      <WorkspaceFileSnapshot base={base} environment={environment} directory={request.directory} entry={preview.entry} mediaType={preview.media_type.split(';')[0]} />
    </>}
  </section>
}

function WorkspaceFileSnapshot({ base, environment, directory, entry, mediaType }: { base: string; environment: Environment; directory: string; entry: WorkspaceEntry; mediaType: string }) {
  const [artifact, setArtifact] = useState<Artifact | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [url, setURL] = useState('')
  const controller = useRef<AbortController | null>(null)
  const objectURL = useRef('')
  const request = useRef<TransferRequest | null>(null)
  const image = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(mediaType)
  useEffect(() => () => { controller.current?.abort(); if (objectURL.current) URL.revokeObjectURL(objectURL.current) }, [])
  async function prepare() {
    if (busy) return
    const active = new AbortController(); controller.current = active
    setBusy(true); setError('')
    try {
      request.current ??= { request_id: nanoid(), source: { environment_id: environment.id, authorization_version: environment.authorization_version, path: entry.path, working_directory: directory }, name: entry.name, media_type: mediaType, visibility: 'agent' }
      let transfer = await api<Transfer>(`${base}/transfers`, request.current, undefined, active.signal)
      for (let i = 0; transfer.state === 'accepted'; i++) {
        if (i >= 120) throw new Error('文件还在准备中。重试会查询同一份下载。')
        await pause(active.signal)
        transfer = await api<Transfer>(`${base}/transfers/${transfer.id}`, undefined, undefined, active.signal)
      }
      if (transfer.state !== 'completed') throw new Error(transfer.error || `文件准备未完成：${transfer.state}`)
      const file = await api<Artifact>(`${base}/artifacts/${transfer.artifact_id}`, undefined, undefined, active.signal)
      setArtifact(file)
      if (image && file.request.manifest.size <= 10 * 1024 * 1024) {
        const blob = await downloadMedia(base, { artifact_id: file.id, sha256: file.request.manifest.sha256, media_type: mediaType }, active.signal)
        objectURL.current = URL.createObjectURL(blob); setURL(objectURL.current)
      }
    } catch (error) { if (!active.signal.aborted) setError(errorText(error)) } finally { if (!active.signal.aborted) setBusy(false) }
  }
  return <div className="management-file-snapshot">{url && <a href={url} target="_blank" rel="noreferrer" aria-label="放大文件图片"><img src={url} alt={entry.name} /></a>}{(!artifact || error) && <Button size="sm" variant="outline" disabled={busy} onClick={() => void prepare()}><Download size={14} />{busy ? '正在准备文件…' : error ? '重试读取文件' : image ? '查看图片与下载' : '准备原文件下载'}</Button>}{artifact && <Button size="sm" variant="outline" asChild><a href={`/api${base}/artifacts/${artifact.id}/download`} download={entry.name}><Download size={14} />下载原文件</a></Button>}{image && artifact && artifact.request.manifest.size > 10 * 1024 * 1024 && <Notice>图片超过 10 MiB，请下载原件查看。</Notice>}{error && <Notice error>{error}</Notice>}</div>
}
