import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveRepositoryLink } from './repositoryLinks'

const root = `https://github.com/test/docs/blob/${'a'.repeat(40)}/`
test('repository references stay on the cited commit and retain anchors', () => {
  assert.equal(resolveRepositoryLink('../guide.md#install', root+'docs/index.md'), root+'guide.md#install')
  assert.equal(resolveRepositoryLink('/README.md', root+'docs/index.md'), root+'README.md')
  assert.equal(resolveRepositoryLink('../../../other', root+'docs/index.md'), null)
  assert.equal(resolveRepositoryLink('#intro', root+'docs/index.md'), '#intro')
  assert.equal(resolveRepositoryLink('https://example.com', root+'docs/index.md'), 'https://example.com')
  assert.equal(resolveRepositoryLink('guide.md', 'https://example.com'), 'guide.md')
})
