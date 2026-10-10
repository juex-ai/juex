import type { MediaRef } from './schema'
import { sha256 } from '@noble/hashes/sha2.js'

const maxImageBytes = 10 * 1024 * 1024

export async function downloadMedia(base: string, media: MediaRef, signal: AbortSignal, send: typeof fetch = fetch): Promise<Blob> {
  signal.throwIfAborted()
  const type = media.media_type?.trim().toLowerCase().replace('image/jpg', 'image/jpeg')
  if (!media.artifact_id || media.artifact_path || !type || !['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(type)) throw new Error('图片引用不可用。')
  const response = await send(`/api${base}/artifacts/${encodeURIComponent(media.artifact_id)}/download`, { credentials: 'same-origin', signal })
  if (!response.ok || !response.body) throw new Error('图片暂不可用，可能已删除或访问权限已改变。')
  if (Number(response.headers.get('Content-Length')) > maxImageBytes) {
    await response.body.cancel()
    throw new Error('图片超过 10 MiB，无法预览。')
  }
  const reader = response.body.getReader()
  const chunks: Uint8Array<ArrayBuffer>[] = []
  let size = 0
  try {
    while (true) {
      signal.throwIfAborted()
      const { done, value } = await reader.read()
      if (done) break
      size += value.byteLength
      if (size > maxImageBytes) throw new Error('图片超过 10 MiB，无法预览。')
      chunks.push(new Uint8Array(value))
    }
  } finally { await reader.cancel(); reader.releaseLock() }
  signal.throwIfAborted()
  const blob = new Blob(chunks, { type })
	const hash = sha256(new Uint8Array(await blob.arrayBuffer()))
	const digest = Array.from(hash, byte => byte.toString(16).padStart(2, '0')).join('')
  if (size === 0 || digest !== media.sha256) throw new Error('图片校验失败，未显示内容。')
  signal.throwIfAborted()
  return blob
}
