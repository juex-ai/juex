import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Copy, Download, LoaderCircle, Upload } from 'lucide-react'
import { nanoid } from 'nanoid'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice } from './components'
import { fileManifest, uploadFile } from './file-transfer'
import { useResource } from './use-resource'
import type { Artifact, ArtifactRequest } from './schema'

function sizeText(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

export function ArtifactDialog({ base, agent, writable, close }: { base: string; agent: string; writable: boolean; close: () => void }) {
  const path = `${base}/artifacts`
  const [revision, setRevision] = useState(0)
  const [after, setAfter] = useState('')
  const refresh = () => setRevision(value => value + 1)
  const files = useResource<Artifact[]>(`${path}?after=${after}`, revision)
  const [file, setFile] = useState<File | null>(null)
  const [visibility, setVisibility] = useState('agent')
  const [resume, setResume] = useState<Artifact | null>(null)
  const [remove, setRemove] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [progress, setProgress] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const controller = useRef<AbortController | null>(null)
  const [request, setRequest] = useState<ArtifactRequest | null>(null)
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => () => controller.current?.abort(), [])

  function reset() { setFile(null); setResume(null); setRequest(null); setProgress(null); if (input.current) input.current.value = '' }
  async function upload(event: FormEvent) {
    event.preventDefault(); if (!file || busy) return
    const active = new AbortController(); controller.current = active
    setBusy(true); setError(''); setNotice(''); setProgress('正在校验文件…')
    try {
      const manifest = await fileManifest(file, active.signal)
      const original = request ?? resume?.request
      if (original && (manifest.size !== original.manifest.size || manifest.sha256 !== original.manifest.sha256)) throw new Error('所选文件与原上传内容不同，请重新选择原文件。')
      const submission = original ?? { request_id: nanoid(), name: file.name, media_type: file.type || 'application/octet-stream', visibility, manifest }
      setRequest(submission)
      const artifact = await uploadFile(path, file, submission, api, active.signal, bytes => setProgress(`上传 ${file.size ? Math.floor(bytes / file.size * 100) : 100}% · ${sizeText(bytes)} / ${sizeText(file.size)}`))
      reset(); setNotice(`${artifact.request.name} 已上传。可复制文件引用发送给 Agent。`)
    } catch (err) { setError(active.signal.aborted ? '上传已停止。重新上传或选择列表中的续传，会从已保存的位置继续。' : errorText(err)) } finally { setBusy(false); setProgress(null); refresh() }
  }
  async function deleteFile(id: string) {
    setBusy(true); setError('')
    try { await api(`${path}/${id}/delete`, {}); setRemove(null); if (resume?.id === id) reset(); refresh() } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  async function copy(artifact: Artifact) {
    try { await navigator.clipboard.writeText(`artifact:${artifact.id}`); setNotice('文件引用已复制，可粘贴到对话中。') } catch { setError('复制失败，请手动复制下方文件引用。') }
  }
  return <Dialog open onOpenChange={open => { if (!open) close() }}><DialogContent className="management-artifact-dialog"><DialogHeader><DialogTitle>文件与产物</DialogTitle><DialogDescription>私有文件仅供此 Agent 使用；Fleet 共享文件可供同一 Fleet 的 Agents 使用。单个文件最多 256 MiB。</DialogDescription></DialogHeader>
    {error && <Notice error>{error}</Notice>}{notice && <Notice>{notice}</Notice>}
    {writable && <form className="management-file-upload" onSubmit={upload}>
      {resume && <Notice>续传 {resume.request.name}：请选择原文件。<Button type="button" variant="ghost" disabled={busy} onClick={reset}>改为新上传</Button></Notice>}
      <Field label={resume ? '原文件' : '选择文件'}><Input ref={input} type="file" disabled={busy} onChange={event => { setFile(event.target.files?.[0] ?? null); setRequest(resume?.request ?? null); setError('') }} /></Field>
      {!resume && <Field label="可见范围"><select value={visibility} disabled={busy || request !== null} onChange={event => setVisibility(event.target.value)}><option value="agent">此 Agent 私有</option><option value="fleet">Fleet 共享</option></select></Field>}
      <div className="management-row-actions"><Button disabled={busy || !file}>{busy ? <LoaderCircle className="animate-spin" /> : <Upload />}{resume || request ? '继续上传' : '上传文件'}</Button>{busy && <Button type="button" variant="outline" onClick={() => controller.current?.abort()}>停止上传</Button>}</div>
      {progress && <p role="status" className="management-file-progress">{progress}</p>}
    </form>}
    <div className="management-artifact-list" aria-label="文件列表">
      {files.error ? <Failure message={files.error} retry={refresh} /> : !files.data ? <Loading /> : files.data.length === 0 ? <Empty title="暂无文件">上传文件后，可在这里下载或复制引用。</Empty> : files.data.map(artifact => <article key={artifact.id} className="management-artifact-row"><div><strong>{artifact.request.name}</strong><small>{sizeText(artifact.request.manifest.size)} · {artifact.request.visibility === 'fleet' ? 'Fleet 共享' : '此 Agent 私有'} · {artifact.state === 'ready' ? '可用' : '上传未完成'}</small>{artifact.state === 'ready' && <code>artifact:{artifact.id}</code>}</div><div className="management-row-actions">
        {artifact.state === 'ready' ? <><Button variant="outline" size="sm" asChild><a href={`/api${path}/${artifact.id}/download`} download><Download />下载</a></Button><Button variant="outline" size="sm" onClick={() => void copy(artifact)}><Copy />引用</Button></> : writable && !artifact.request.source && <Button variant="outline" size="sm" disabled={busy} onClick={() => { reset(); setResume(artifact); setError(''); input.current?.focus() }}>续传</Button>}
        {writable && artifact.scope.agent_id === agent && (remove === artifact.id ? <><span>永久删除此文件？</span><Button variant="destructive" size="sm" disabled={busy} onClick={() => void deleteFile(artifact.id)}>确认删除</Button><Button variant="ghost" size="sm" disabled={busy} onClick={() => setRemove(null)}>取消</Button></> : <Button variant="ghost" size="sm" disabled={busy} onClick={() => setRemove(artifact.id)}>删除</Button>)}
      </div></article>)}
    </div>
    <div className="management-row-actions">{after && <Button variant="ghost" onClick={() => setAfter('')}>回到第一页</Button>}{files.data?.length === 100 && <Button variant="outline" onClick={() => setAfter(files.data![files.data!.length - 1].id)}>下一页</Button>}<Button variant="outline" onClick={close}>关闭</Button></div>
  </DialogContent></Dialog>
}
