import assert from 'node:assert/strict'
import test from 'node:test'
import { createDraftStore } from '../../frontend/src/management/draft-store.ts'

function storage() {
  const values = new Map<string, string>()
  return { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value) }, removeItem: (key: string) => { values.delete(key) } }
}
const request = { request_id: 'request', thread_id: 'main', text: 'first' }

for (const edit of ['text', 'image', 'both']) test(`late receipt settles submitted text and images independently after ${edit} edits`, () => {
  const disk = storage()
  const store = createDraftStore(disk)
  const media = { artifact_id: 'original', sha256: 'a'.repeat(64), media_type: 'image/png', size: 100 }
  store.edit('main', request.text)
  store.addImage('main', { id: 'original-upload', name: 'original.png', state: 'ready', progress: 1, media })
  store.submit('main', { ...request, images: [media] })
  if (edit !== 'image') store.edit('main', 'next text')
  if (edit !== 'text') store.addImage('main', { id: 'next-upload', name: 'next.png', state: 'uploading', progress: 0 })
  const restored = createDraftStore(disk)
  restored.accept('main', request.request_id)
  assert.equal(restored.read('main').text, edit === 'image' ? '' : 'next text')
  assert.deepEqual(restored.read('main').images.map(image => image.id), edit === 'text' ? [] : ['next-upload'])
})

test('uploaded images and unknown admission keep ordered references across reload', () => {
  const disk = storage()
  const store = createDraftStore(disk)
  const media = { artifact_id: 'image', sha256: 'a'.repeat(64), media_type: 'image/png', size: 100 }
  store.addImage('main', { id: 'upload', name: 'screenshot.png', state: 'uploading', progress: 0 })
  const revision = store.read('main').revision
  store.updateImage('main', 'upload', { state: 'ready', progress: 1, media })
  assert.equal(store.read('main').revision, revision)
  const input = { ...request, text: '', images: [media] }
  store.submit('main', input)
  const restored = createDraftStore(disk)
  assert.deepEqual(restored.read('main').images[0].media, media)
  assert.deepEqual(restored.read('main').pending?.request, input)
  restored.removeImage('main', 'upload')
  restored.addImage('main', { id: 'next', name: 'next.png', state: 'uploading', progress: 0 })
  restored.accept('main', input.request_id)
  assert.equal(restored.read('main').images[0].id, 'next')
  restored.updateImage('main', 'upload', { state: 'ready', media })
  assert.equal(restored.read('main').images.length, 1)
})

test('incomplete uploads after reload require reselection instead of claiming a ready image', () => {
  const disk = storage()
  const store = createDraftStore(disk)
  store.addImage('main', { id: 'upload', name: 'image.png', state: 'uploading', progress: 0.5 })
  const restored = createDraftStore(disk).read('main')
  assert.equal(restored.images[0].state, 'failed')
  assert.match(restored.images[0].error!, /重新选择/)
  assert.equal(restored.images[0].media, undefined)
})

test('a missing draft restores pending image identities so acceptance clears exactly those images', () => {
  const disk = storage()
  const store = createDraftStore(disk)
  const media = { artifact_id: 'artifact', sha256: 'a'.repeat(64), media_type: 'image/png', size: 100 }
  store.edit('main', 'sent text')
  store.addImage('main', { id: 'upload', name: 'image.png', state: 'ready', progress: 1, media })
  store.submit('main', { ...request, text: 'sent text', images: [media] })
  disk.setItem('juex.draft:main', '{broken')
  const restored = createDraftStore(disk)
  assert.deepEqual(restored.read('main').pending?.request.images, [media])
  restored.accept('main', request.request_id)
  assert.equal(restored.read('main').images.length, 0)
  assert.equal(restored.read('main').text, '')
})

test('a corrupted draft never discards a valid pending admission identity', () => {
  const disk = storage()
  disk.setItem('juex.draft:main', '{broken')
  disk.setItem('juex.pending:main', JSON.stringify(request))
  const store = createDraftStore(disk)
  assert.deepEqual(store.read('main').pending?.request, request)
  assert.equal(store.read('main').text, 'first')
})

test('drafts survive route changes and isolate actor, tenant, Agent and Thread', () => {
  const store = createDraftStore(storage())
  const keys = ['actor:tenant:agent:main', 'actor:tenant:agent:worker', 'actor:tenant:other:main', 'other:tenant:agent:main', 'actor:other:agent:main']
  keys.forEach((key, index) => store.edit(key, `draft ${index}`))
  keys.reverse().forEach((key, index) => assert.equal(store.read(key).text, `draft ${4 - index}`))
  assert.equal(store.read('new').text, '')
})

test('acceptance clears only the submitted revision, including edits back to identical text', () => {
  const store = createDraftStore(storage())
  store.edit('main', 'first')
  store.submit('main', request)
  store.edit('main', 'next')
  store.edit('main', 'first')
  store.accept('main', request.request_id)
  assert.equal(store.read('main').text, 'first')
  assert.equal(store.read('main').pending, null)
  store.submit('main', { ...request, request_id: 'second' })
  store.accept('main', 'request')
  assert.equal(store.read('main').pending?.request.request_id, 'second')
  store.accept('main', 'second')
  assert.equal(store.read('main').text, '')
})

test('unknown submission survives reload without replacing a newer draft or retry identity', () => {
  const disk = storage()
  const before = createDraftStore(disk)
  before.edit('main', 'first')
  before.submit('main', request)
  before.edit('main', 'next')
  const after = createDraftStore(disk)
  assert.equal(after.read('main').text, 'next')
  assert.deepEqual(after.submit('main', { ...request, request_id: 'wrong' }), request)
  after.accept('main', request.request_id)
  assert.equal(after.read('main').text, 'next')
  assert.equal(createDraftStore(disk).read('main').pending, null)
})

test('definite rejection preserves the draft and allows a new request', () => {
  const store = createDraftStore(storage())
  store.edit('main', 'first')
  store.submit('main', request)
  store.reject('main', 'request')
  assert.equal(store.read('main').text, 'first')
  assert.equal(store.read('main').pending, null)
  assert.equal(store.submit('main', { ...request, request_id: 'retry' }).request_id, 'retry')
})

test('denied browser storage retains drafts and receipts in the mounted application', () => {
  const fail = () => { throw new Error('storage disabled') }
  const store = createDraftStore({ getItem: fail, setItem: fail, removeItem: fail })
  store.edit('main', 'first')
  store.submit('main', request)
  assert.equal(store.read('main').text, 'first')
  assert.equal(store.read('main').pending?.request.request_id, 'request')
  store.accept('main', 'request')
  assert.equal(store.read('main').text, '')
})
