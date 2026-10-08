import { sha256 } from '@noble/hashes/sha2.js'
import type { Artifact, ArtifactRequest, ArtifactUpload, FileChunk, FileManifest } from './schema'

export const fileChunkBytes = 256 * 1024
export const maxFileBytes = 256 * 1024 * 1024

export type FileTransport = <T>(path: string, body?: unknown, method?: string, signal?: AbortSignal) => Promise<T>

export async function fileManifest(file: File, signal: AbortSignal): Promise<FileManifest> {
  if (file.size > maxFileBytes) throw new Error('单个文件最多支持 256 MiB。')
  signal.throwIfAborted()
  const data = await file.arrayBuffer()
  signal.throwIfAborted()
  const sha256 = await digest(data)
  signal.throwIfAborted()
  return { size: file.size, sha256 }
}

async function digest(data: ArrayBuffer): Promise<string> {
  return Array.from(sha256(new Uint8Array(data)), byte => byte.toString(16).padStart(2, '0')).join('')
}

function base64(data: ArrayBuffer): string {
  const bytes = new Uint8Array(data)
  let text = ''
  for (let offset = 0; offset < bytes.length; offset += 8192) text += String.fromCharCode(...bytes.subarray(offset, offset + 8192))
  return btoa(text)
}

// The request is retained by the caller across failures. Begin returns the
// durable byte cursor, so a lost response cannot duplicate an upload.
export async function uploadFile(base: string, file: File, request: ArtifactRequest, send: FileTransport, signal: AbortSignal, progress: (bytes: number) => void): Promise<Artifact> {
  signal.throwIfAborted()
  let upload = await send<ArtifactUpload>(base, request, 'POST', signal)
  if (upload.artifact.state === 'ready') return upload.artifact
  if (!Number.isSafeInteger(upload.cursor) || upload.cursor < 0 || upload.cursor > file.size) throw new Error('服务器返回了无效的上传进度。')
  progress(upload.cursor)
  const path = `${base}/${upload.artifact.id}`
  while (upload.cursor < file.size) {
    signal.throwIfAborted()
    const offset = upload.cursor
    const data = await file.slice(offset, Math.min(file.size, offset + fileChunkBytes)).arrayBuffer()
    const chunk: FileChunk = { offset, data: base64(data), sha256: await digest(data) }
    upload = await send<ArtifactUpload>(`${path}/chunks`, chunk, 'PUT', signal)
    if (upload.cursor !== offset + data.byteLength) throw new Error('服务器返回了无效的上传进度。')
    progress(upload.cursor)
  }
  signal.throwIfAborted()
  return send<Artifact>(`${path}/commit`, {}, 'POST', signal)
}
