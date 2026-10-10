import { nanoid } from 'nanoid'
import { api, errorText } from './api'
import { fileManifest, uploadFile } from './file-transfer'
import type { createDraftStore } from './draft-store'

type DraftStore = ReturnType<typeof createDraftStore>
const activeUploads = new Map<string, AbortController>()
export const imageAccept = 'image/png,image/jpeg,image/gif,image/webp'

export function addDraftImages(store: DraftStore, key: string, base: string, files: File[]) {
  if (store.read(key).images.length + files.length > 8) throw new Error('每条消息最多添加 8 张图片。')
  for (const file of files) {
    if (!imageAccept.split(',').includes(file.type)) throw new Error('请选择 PNG、JPEG、GIF 或 WebP 图片。')
    if (file.size === 0 || file.size > 10 * 1024 * 1024) throw new Error('每张图片需在 10 MiB 以内，且不能为空。')
  }
  for (const file of files) {
    const id = nanoid()
    if (store.addImage(key, { id, name: file.name || '图片', file, state: 'uploading', progress: 0 })) void uploadDraftImage(store, key, base, id)
  }
}

export async function uploadDraftImage(store: DraftStore, key: string, base: string, id: string) {
  const job = `${key}:${id}`
  const image = store.read(key).images.find(image => image.id === id)
  if (!image?.file || activeUploads.has(job)) return
  const controller = new AbortController()
  activeUploads.set(job, controller)
  store.updateImage(key, id, { state: 'uploading', error: undefined })
  try {
    const request = image.request ?? { request_id: id, name: image.name, media_type: image.file.type, visibility: 'agent', manifest: await fileManifest(image.file, controller.signal) }
    store.updateImage(key, id, { request })
    const artifact = await uploadFile(`${base}/artifacts`, image.file, request, api, controller.signal, bytes => store.updateImage(key, id, { progress: bytes / image.file!.size }))
    controller.signal.throwIfAborted()
    store.updateImage(key, id, { state: 'ready', progress: 1, media: { artifact_id: artifact.id, sha256: request.manifest.sha256, media_type: request.media_type, size: request.manifest.size } })
  } catch (error) {
    if (!controller.signal.aborted) store.updateImage(key, id, { state: 'failed', error: errorText(error) })
  } finally { activeUploads.delete(job) }
}

export function removeDraftImage(store: DraftStore, key: string, id: string) {
  activeUploads.get(`${key}:${id}`)?.abort()
  store.removeImage(key, id)
}
