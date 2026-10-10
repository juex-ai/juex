import { useEffect, useState } from 'react'
import { Image, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { downloadMedia } from './media-download'
import type { DraftImage } from './draft-store'

function Thumbnail({ image, base }: { image: DraftImage; base: string }) {
  const [url, setURL] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    let objectURL = ''
    async function load() {
      const blob = image.file ?? (image.media ? await downloadMedia(base, image.media, controller.signal) : null)
      if (!blob || controller.signal.aborted) return
      objectURL = URL.createObjectURL(blob)
      setURL(objectURL)
    }
    void load().catch(() => { /* Admission and transcript retain explicit media errors. */ })
    return () => { controller.abort(); if (objectURL) URL.revokeObjectURL(objectURL) }
  }, [image.file, image.media, base])
  return url ? <img src={url} alt={image.name} /> : <Image size={24} aria-hidden />
}

export function ComposerImages({ images, base, remove, retry }: { images: DraftImage[]; base: string; remove: (id: string) => void; retry: (id: string) => void }) {
  return <ul className="management-composer-images" aria-label="待发送图片">{images.map(image => <li key={image.id}>
    <Thumbnail image={image} base={base} /><div><strong>{image.name}</strong><small role="status">{image.state === 'ready' ? '已上传' : image.state === 'uploading' ? `上传中 ${Math.round(image.progress * 100)}%` : image.error}</small>{image.state === 'failed' && image.file && <Button size="sm" type="button" variant="ghost" onClick={() => retry(image.id)}>重试上传</Button>}</div>
    <Button type="button" variant="ghost" size="icon" aria-label={`移除 ${image.name}`} onClick={() => remove(image.id)}><X size={14} /></Button>
  </li>)}</ul>
}
