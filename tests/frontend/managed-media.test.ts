import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import test from 'node:test'
import { downloadMedia } from '../../frontend/src/management/media-download.ts'

const bytes = Uint8Array.from(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg==', 'base64'))
const ref = { artifact_id: 'image-id', sha256: createHash('sha256').update(bytes).digest('hex'), media_type: 'image/png', original_bytes: 1000000 }

test('history image uses an authenticated bounded download and verifies the original reference', async () => {
  const controller = new AbortController()
  const send: typeof fetch = async (path, options) => {
    assert.equal(path, '/api/tenants/t/agents/a/artifacts/image-id/download')
    assert.equal(options?.credentials, 'same-origin')
    assert.equal(options?.signal, controller.signal)
    return new Response(bytes, { headers: { 'Content-Type': 'image/png' } })
  }
  const blob = await downloadMedia('/tenants/t/agents/a', ref, controller.signal, send)
  assert.equal(blob.type, 'image/png')
  assert.deepEqual(new Uint8Array(await blob.arrayBuffer()), bytes)
})

test('history image rejects missing files, changed bytes, unsafe formats and oversized responses', async () => {
  const signal = new AbortController().signal
  for (const response of [new Response('{}', { status: 403 }), new Response('wrong bytes'), new Response(bytes, { headers: { 'Content-Length': String(11 * 1024 * 1024) } })]) {
    await assert.rejects(downloadMedia('/agent', ref, signal, async () => response))
  }
  await assert.rejects(downloadMedia('/agent', { ...ref, media_type: 'image/svg+xml' }, signal, async () => new Response(bytes)))
  await assert.rejects(downloadMedia('/agent', { ...ref, artifact_id: undefined, artifact_path: 'old/path' }, signal, async () => { assert.fail('old local path must not be fetched') }))
  const aborted = AbortSignal.abort()
  await assert.rejects(downloadMedia('/agent', ref, aborted, async () => { assert.fail('cancelled image must not be fetched') }))
})
