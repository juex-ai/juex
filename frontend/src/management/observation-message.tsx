import { useCallback, useEffect, useRef, useState } from 'react'
import { sha256 } from '@noble/hashes/sha2.js'
import { Button } from '@/components/ui/button'
import { writeClipboardText } from '@/lib/clipboard'
import { api, errorText } from './api'
import { Failure, Loading, Notice } from './components'
import { MessageMedia } from './message-media'
import { useResource } from './use-resource'
import type { Artifact, ObservationContent } from './schema'

type Attachment = { artifact_id?: string; transfer_id?: string; name?: string; error?: string }
type Payload = { full_content?: string; content?: string; attachments?: Attachment[] }

export function ObservationMessage({ base, id, fallback }: { base: string; id: string; fallback: string }) {
  const [open, setOpen] = useState(false)
  return <details className="management-tool-row" onToggle={event => setOpen(event.currentTarget.open)}><summary>外部观察事件 · 数据</summary>{open && <ObservationBody key={`${base}:${id}`} base={base} id={id} fallback={fallback} />}</details>
}

function ObservationBody({ base, id, fallback }: { base: string; id: string; fallback: string }) {
  const [value, setValue] = useState<{ page: ObservationContent; text: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const controller = useRef<AbortController | null>(null)
  const load = useCallback(async (previous: { page: ObservationContent; text: string } | null = null) => {
    const active = new AbortController(); controller.current?.abort(); controller.current = active
    setBusy(true); setError('')
    try {
      const page = await api<ObservationContent>(`${base}/observations/${encodeURIComponent(id)}?offset=${previous?.page.next_offset ?? 0}&limit=16384`, undefined, undefined, active.signal)
      if (page.id !== id || page.offset !== (previous?.page.next_offset ?? 0) || page.next_offset < page.offset || page.has_more && page.next_offset === page.offset || previous && page.total_characters !== previous.page.total_characters) throw new Error('事件内容分页不一致，请重新打开。')
      if (!active.signal.aborted) { setValue({ page, text: (previous?.text ?? '') + page.data }); setCopied(false) }
    } catch (err) { if (!active.signal.aborted) setError(errorText(err)) } finally { if (!active.signal.aborted) setBusy(false) }
  }, [base, id])
  useEffect(() => { void load(); return () => controller.current?.abort() }, [load])
  let payload: Payload | null = null
  if (value && !value.page.has_more) { try { payload = JSON.parse(value.text) as Payload } catch { /* Keep the original data visible. */ } }
  const body = value?.page.kind === 'command.observation' && payload ? payload.full_content ?? payload.content ?? value.text : value?.text
  const attachments = value?.page.kind === 'command.observation' && Array.isArray(payload?.attachments) ? payload.attachments : []
  return <div className="management-observation-body">
    {value && <p className="management-file-location">{value.page.kind} · {new Date(value.page.created_at).toLocaleString()}<span>原始环境 <code>{value.page.environment_id}</code></span>{value.page.operation_id && <span>原始操作 <code>{value.page.operation_id}</code></span>}<span>事件 <code>{id}</code></span></p>}
    {body !== undefined ? <pre>{body}</pre> : <pre>{fallback}</pre>}
    {error && <Failure message={error} retry={() => void load(value)} />}
    {busy && <Loading />}
    {value?.page.has_more && <><Notice>已加载 {value.page.next_offset.toLocaleString()} / {value.page.total_characters.toLocaleString()} 字符；目前显示原始 JSON 片段，完整加载后显示正文与冻结附件。</Notice><Button size="sm" variant="outline" disabled={busy} onClick={() => void load(value)}>加载更多事件内容</Button></>}
    {body !== undefined && <Button size="sm" variant="ghost" onClick={() => void writeClipboardText(body).then(() => setCopied(true)).catch(err => setError(errorText(err)))}>{copied ? '已复制事件内容' : value?.page.has_more ? '复制已加载部分' : '复制事件全文'}</Button>}
    {attachments.map((attachment, index) => <ObservationAttachment key={`${index}:${attachment.artifact_id ?? ''}`} base={base} value={attachment} />)}
  </div>
}

function ObservationAttachment({ base, value }: { base: string; value: Attachment }) {
  if (!value.artifact_id || !/^[\da-f]{8}-[\da-f-]{27}$/i.test(value.artifact_id)) return <Notice error>附件 {value.name || '未命名'} 未冻结：{value.error || '未取得可读取的文件产物。'}{value.transfer_id && <code>{value.transfer_id}</code>}</Notice>
  return <CapturedArtifact base={base} id={value.artifact_id} name={value.name} />
}

function CapturedArtifact({ base, id, name }: { base: string; id: string; name?: string }) {
  const [revision, setRevision] = useState(0)
  const file = useResource<Artifact>(`${base}/artifacts/${id}`, revision)
  if (file.error) return <Failure message={`附件 ${name || id}：${file.error}`} retry={() => setRevision(value => value + 1)} />
  if (!file.data) return <Loading />
  const artifact = file.data
  if (artifact.state !== 'ready' || artifact.id !== id) return <Notice error>附件尚不可读。</Notice>
  const type = artifact.request.media_type.split(';')[0]
  return <section className="management-observation-attachment" aria-label={`事件附件 ${name || artifact.request.name}`}><strong>{name || artifact.request.name}</strong><small>{artifact.request.manifest.size.toLocaleString()} B · 冻结文件产物</small>
    {['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(type) ? <MessageMedia base={base} media={{ artifact_id: id, sha256: artifact.request.manifest.sha256, media_type: type }} /> : <>
      {(type.startsWith('text/') || ['application/json', 'application/xml'].includes(type)) && <ArtifactText base={base} artifact={artifact} />}
      <Button size="sm" variant="outline" asChild><a href={`/api${base}/artifacts/${id}/download`} download={artifact.request.name}>下载事件附件</a></Button>
    </>}
  </section>
}

function ArtifactText({ base, artifact }: { base: string; artifact: Artifact }) {
  const [result, setResult] = useState<{ text: string; truncated: boolean } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  async function read() {
    const active = new AbortController(); controller.current = active
    setBusy(true); setError('')
    try {
      const response = await fetch(`/api${base}/artifacts/${artifact.id}/download`, { credentials: 'same-origin', signal: active.signal })
      if (!response.ok || !response.body) throw new Error('附件暂不可用，可能已删除或访问权限改变。')
      const reader = response.body.getReader(), chunks: Uint8Array[] = []
      let size = 0, truncated = false
      try { for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        const take = Math.min(value.length, 256 * 1024 - size)
        chunks.push(value.slice(0, take)); size += take
        if (size === 256 * 1024) { truncated = value.length > take || artifact.request.manifest.size > size; break }
      } } finally { await reader.cancel(); reader.releaseLock() }
      const bytes = new Uint8Array(size); let offset = 0
      for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length }
      if (!truncated && (size !== artifact.request.manifest.size || Array.from(sha256(bytes), byte => byte.toString(16).padStart(2, '0')).join('') !== artifact.request.manifest.sha256)) throw new Error('附件校验失败，未显示内容。')
      const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes, { stream: truncated })
      if (text.includes('\0')) throw new Error('附件不是可预览的 UTF-8 文本，请下载原件。')
      if (!active.signal.aborted) setResult({ text, truncated })
    } catch (err) { if (!active.signal.aborted) setError(errorText(err)) } finally { if (!active.signal.aborted) setBusy(false) }
  }
  return <>{result && <><pre>{result.text}</pre>{result.truncated && <Notice>仅预览前 256 KiB，未验证完整文件哈希；下载原件可查看全部内容。</Notice>}</>}{error && <Notice error>{error}</Notice>}{(!result || error) && <Button size="sm" variant="outline" disabled={busy} onClick={() => void read()}>{busy ? '正在读取附件…' : error ? '重试附件预览' : '预览事件附件'}</Button>}</>
}
