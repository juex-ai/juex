import assert from 'node:assert/strict'
import test from 'node:test'
import { localMarkdownPath } from '../../frontend/src/management/markdown-path.ts'

test('Markdown targets preserve Unicode and resolve only relative workspace paths', () => {
  assert.equal(localMarkdownPath('./notes/%E8%AF%81%E6%8D%AE%20one.txt'), 'notes/证据 one.txt')
  assert.equal(localMarkdownPath('images/plot.png'), 'images/plot.png')
  for (const value of ['', '/', '/tmp/a', '//host/a', 'https://host/a', 'file:///a', 'data:x', 'javascript:alert(1)', '../a', 'a/../../b', 'a/%2e%2e/b', '%2fetc/passwd', '%66ile%3a/a', '#anchor', '?query', 'a\\b', 'a%00b', 'a//b', '%bad', ' a']) assert.equal(localMarkdownPath(value), null, value)
})
