import test from 'node:test'
import assert from 'node:assert/strict'
import { api, APIError, errorText } from '../../frontend/src/management/api.ts'

for (const status of [502, 504]) {
  test(`HTML gateway ${status} retains status without exposing the response body`, async t => {
    t.mock.method(globalThis, 'fetch', async () => new Response('<html>private upstream details</html>', { status }))
    await assert.rejects(api('/environments'), error => {
      assert.ok(error instanceof APIError)
      assert.equal(error.status, status)
      assert.match(errorText(error), new RegExp(`HTTP ${status}`))
      assert.doesNotMatch(errorText(error), /private|html|Unexpected token/)
      return true
    })
  })
}

test('JSON application errors retain their code and translated message', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ code: 'access_denied', error: 'denied' }, { status: 403 }))
  await assert.rejects(api('/environments'), error => {
    assert.ok(error instanceof APIError)
    assert.equal(error.status, 403)
    assert.equal(error.code, 'access_denied')
    assert.equal(errorText(error), '没有权限访问此资源，或成员状态已改变。')
    return true
  })
})

test('a malformed successful response reports an invalid response', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response('<html>not application data</html>'))
  await assert.rejects(api('/environments'), error => error instanceof APIError && error.status === 200 && error.code === 'invalid_response')
})

test('body cancellation remains cancellation rather than an HTTP error', async t => {
  const aborted = new DOMException('request cancelled', 'AbortError')
  t.mock.method(globalThis, 'fetch', async () => ({ status: 200, ok: true, json: async () => { throw aborted } }))
  await assert.rejects(api('/environments'), error => error === aborted)
})
