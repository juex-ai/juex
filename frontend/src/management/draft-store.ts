import type { ArtifactRequest, InputImage, InputRequest } from './schema'

type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem' | 'removeItem'>
export type DraftImage = { id: string; name: string; state: 'uploading' | 'ready' | 'failed'; progress: number; error?: string; file?: File; request?: ArtifactRequest; media?: InputImage }
export type Draft = { text: string; images: DraftImage[]; revision: number; pending: { request: InputRequest; revision: number; imageIDs: string[] } | null }

function savedImages(value: unknown): DraftImage[] {
  if (!Array.isArray(value)) return []
  return value.slice(0, 8).filter(item => typeof item?.id === 'string' && typeof item.name === 'string').map(item => item.state === 'ready' && item.media?.artifact_id && item.media?.sha256 ? { id: item.id, name: item.name, state: 'ready', progress: 1, media: item.media } : { id: item.id, name: item.name, state: 'failed', progress: 0, error: '页面刷新前上传尚未完成，请移除后重新选择图片。' })
}

// Text edits and image identities settle independently: adding another image
// cannot make a late receipt resubmit the preceding text or attachments.
export function createDraftStore(storage?: Storage) {
  const drafts = new Map<string, Draft>()
  const listeners = new Set<() => void>()
  function stored(key: string) {
    try { return JSON.parse(storage?.getItem(key) ?? 'null') }
    catch { return null }
  }
  function read(key: string): Draft {
    const cached = drafts.get(key)
    if (cached) return cached
    let draft: Draft = { text: '', images: [], revision: 0, pending: null }
      const saved = stored(`juex.draft:${key}`)
      const hasDraft = typeof saved?.text === 'string' && Number.isSafeInteger(saved.revision)
      if (hasDraft) draft = { ...draft, text: saved.text, images: savedImages(saved.images), revision: saved.revision }
      const pending = stored(`juex.pending:${key}`)
      if (typeof pending?.request_id === 'string' && typeof pending.text === 'string' && typeof pending.thread_id === 'string') {
        if (!hasDraft) {
          draft.text = pending.text
          draft.revision = pending.draft_revision ?? 0
          draft.images = (pending.images ?? []).map((media: InputImage, index: number) => ({ id: pending.draft_image_ids?.[index] ?? media.artifact_id, name: '待确认图片', state: 'ready', progress: 1, media }))
        }
        draft.pending = { request: { request_id: pending.request_id, thread_id: pending.thread_id, text: pending.text, ...(pending.images?.length ? { images: pending.images } : {}) }, revision: pending.draft_revision ?? (pending.text === draft.text ? draft.revision : -1), imageIDs: pending.draft_image_ids ?? draft.images.filter(image => pending.images?.some((ref: InputImage) => ref.artifact_id === image.media?.artifact_id)).map(image => image.id) }
      }
    drafts.set(key, draft)
    return draft
  }
  function write(key: string, draft: Draft) {
    drafts.set(key, draft)
    try {
      if (draft.text || draft.pending || draft.images.length) storage?.setItem(`juex.draft:${key}`, JSON.stringify({ text: draft.text, revision: draft.revision, images: draft.images.map(({ id, name, state, media }) => ({ id, name, state, media })) }))
      else storage?.removeItem(`juex.draft:${key}`)
      if (draft.pending) storage?.setItem(`juex.pending:${key}`, JSON.stringify({ ...draft.pending.request, draft_revision: draft.pending.revision, draft_image_ids: draft.pending.imageIDs }))
      else storage?.removeItem(`juex.pending:${key}`)
    } catch { /* Quota or privacy restrictions must not discard in-memory work. */ }
    listeners.forEach(listener => listener())
  }
  return {
    read,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener) } },
    edit(key: string, text: string) {
      const draft = read(key)
      if (draft.text !== text) write(key, { ...draft, text, revision: draft.revision + 1 })
    },
    addImage(key: string, image: DraftImage) {
      const draft = read(key)
      if (draft.images.length >= 8) return false
      write(key, { ...draft, images: [...draft.images, image] })
      return true
    },
    updateImage(key: string, id: string, update: Partial<DraftImage>) {
      const draft = read(key)
      if (draft.images.some(image => image.id === id)) write(key, { ...draft, images: draft.images.map(image => image.id === id ? { ...image, ...update } : image) })
    },
    removeImage(key: string, id: string) {
      const draft = read(key)
      write(key, { ...draft, images: draft.images.filter(image => image.id !== id) })
    },
    submit(key: string, request: InputRequest): InputRequest {
      const draft = read(key)
      if (draft.pending) return draft.pending.request
      write(key, { ...draft, pending: { request, revision: draft.revision, imageIDs: draft.images.map(image => image.id) } })
      return request
    },
    accept(key: string, requestID: string) {
      const draft = read(key)
      if (draft.pending?.request.request_id !== requestID) return
      write(key, { ...draft, text: draft.revision === draft.pending.revision ? '' : draft.text, images: draft.images.filter(image => !draft.pending!.imageIDs.includes(image.id)), pending: null })
    },
    reject(key: string, requestID: string) {
      const draft = read(key)
      if (draft.pending?.request.request_id === requestID) write(key, { ...draft, pending: null })
    },
  }
}
