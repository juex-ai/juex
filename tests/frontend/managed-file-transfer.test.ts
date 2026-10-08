import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash, webcrypto } from 'node:crypto'
import { fileChunkBytes, fileManifest, uploadFile, type FileTransport } from '../../frontend/src/management/file-transfer.ts'
import type { Artifact, ArtifactRequest, FileChunk } from '../../frontend/src/management/schema.ts'

test('HTTP without SubtleCrypto preserves binary manifest and chunk checksums', async () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto')!
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) } })
  try {
    const bytes = Buffer.alloc(fileChunkBytes + 13)
    for (let i = 0; i < bytes.length; i++) bytes[i] = i % 251
    const file = new File([bytes], 'HTTP.bin')
    const signal = new AbortController().signal
    const manifest = await fileManifest(file, signal)
    assert.equal(manifest.sha256, createHash('sha256').update(bytes).digest('hex'))
    const request = { request_id: 'http', name: file.name, manifest } as ArtifactRequest
    const artifact = { id: 'http-artifact', state: 'uploading', request } as Artifact
    let chunks = 0
    const send = (async (path: string, body: unknown) => {
      if (path === '/files') return { artifact, cursor: 0 }
      if (path.endsWith('/chunks')) {
        chunks++
        const chunk = body as FileChunk
        const data = Buffer.from(chunk.data, 'base64')
        assert.equal(chunk.sha256, createHash('sha256').update(data).digest('hex'))
        return { artifact, cursor: chunk.offset + data.length }
      }
      return { ...artifact, state: 'ready' }
    }) as FileTransport
    assert.equal((await uploadFile('/files', file, request, send, signal, () => {})).state, 'ready')
    assert.equal(chunks, 2)
  } finally {
    Object.defineProperty(globalThis, 'crypto', descriptor)
  }
})

test('binary upload resumes the durable cursor after a lost response', async () => {
  const data = new Uint8Array(fileChunkBytes * 2 + 17)
  for (let i = 0; i < data.length; i++) data[i] = i % 256
  const file = new File([data], '产物.bin')
  const signal = new AbortController().signal
  const request: ArtifactRequest = { request_id: 'stable-request', name: file.name, media_type: 'application/octet-stream', visibility: 'agent', manifest: await fileManifest(file, signal) }
  const artifact = { id: 'object-id', state: 'uploading', request } as Artifact
  let cursor = 0
  let lost = true
  const received = new Uint8Array(data.length)
  const offsets: number[] = []
  const send = (async (path: string, body: unknown) => {
    if (path === '/files') { assert.deepEqual(body, request); return { artifact, cursor } }
    if (path.endsWith('/chunks')) {
      const chunk = body as FileChunk
      assert.equal(chunk.offset, cursor)
      const bytes = Buffer.from(chunk.data, 'base64')
      assert.ok(bytes.length <= fileChunkBytes)
      received.set(bytes, cursor); offsets.push(cursor); cursor += bytes.length
      if (lost) { lost = false; throw new Error('response lost') }
      return { artifact, cursor }
    }
    assert.equal(path, '/files/object-id/commit')
    assert.equal(cursor, data.length)
    return { ...artifact, state: 'ready' }
  }) as FileTransport
  await assert.rejects(uploadFile('/files', file, request, send, signal, () => {}), /response lost/)
  const result = await uploadFile('/files', file, request, send, signal, () => {})
  assert.equal(result.state, 'ready')
  assert.deepEqual(offsets, [0, fileChunkBytes, fileChunkBytes * 2])
  assert.deepEqual(received, data)
})

test('empty file commits without chunks; cancellation does not create a new upload', async () => {
  const file = new File([], 'empty.txt')
  const controller = new AbortController()
  const manifest = await fileManifest(file, controller.signal)
  assert.equal(manifest.sha256, 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
  const request: ArtifactRequest = { request_id: 'empty', name: file.name, media_type: 'text/plain', visibility: 'fleet', manifest }
  let calls = 0
  const send = (async (path: string) => {
    calls++
    if (path === '/files') return { artifact: { id: 'empty', state: 'uploading', request }, cursor: 0 }
    assert.equal(path, '/files/empty/commit')
    return { id: 'empty', state: 'ready', request }
  }) as FileTransport
  await uploadFile('/files', file, request, send, controller.signal, () => {})
  assert.equal(calls, 2)
  controller.abort()
  await assert.rejects(uploadFile('/files', file, request, send, controller.signal, () => {}), { name: 'AbortError' })
  assert.equal(calls, 2)
})
