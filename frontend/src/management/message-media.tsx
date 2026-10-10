import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { errorText } from './api'
import { downloadMedia } from './media-download'
import type { MediaRef } from './schema'

export function MessageMedia({ base, media }: { base: string; media?: MediaRef | null }) {
  const [url, setURL] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [zoom, setZoom] = useState(false)
  const controller = useRef<AbortController | null>(null)
  const objectURL = useRef('')
  useEffect(() => () => { controller.current?.abort(); if (objectURL.current) URL.revokeObjectURL(objectURL.current) }, [])
  async function load() {
    if (!media || busy) return
    const active = new AbortController(); controller.current = active
    setBusy(true); setError('')
    try {
      const blob = await downloadMedia(base, media, active.signal)
      if (active.signal.aborted) return
      if (objectURL.current) URL.revokeObjectURL(objectURL.current)
      objectURL.current = URL.createObjectURL(blob)
      setURL(objectURL.current)
    } catch (err) { if (!active.signal.aborted) setError(errorText(err)) } finally { if (!active.signal.aborted) setBusy(false) }
  }
  if (!media?.artifact_id) return <p className="management-media-error">图片引用不可用。</p>
  return <figure className="management-message-media">
    {url && !error && <button type="button" aria-label="放大对话图片" onClick={() => setZoom(true)}><img src={url} alt="对话图片" onError={() => setError('图片无法解码，未显示内容。')} /></button>}
    {error && <p role="status" className="management-media-error">{error}</p>}
    <figcaption className="management-row-actions">
      {(!url || error) && <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void load()}>{busy ? '正在读取图片…' : error ? '重试图片' : '查看图片'}</Button>}
      <Button size="sm" variant="ghost" asChild><a href={`/api${base}/artifacts/${encodeURIComponent(media.artifact_id)}/download`} download>下载图片</a></Button>
    </figcaption>
    {url && !error && <Dialog open={zoom} onOpenChange={setZoom}><DialogContent className="management-image-lightbox"><DialogHeader><DialogTitle>对话图片</DialogTitle><DialogDescription>已核对图片内容，可查看原图或下载保存。</DialogDescription></DialogHeader><a href={url} target="_blank" rel="noreferrer" aria-label="打开原图"><img src={url} alt="放大的对话图片" /></a></DialogContent></Dialog>}
  </figure>
}
