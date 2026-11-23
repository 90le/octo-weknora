import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { DOC_PAGES, docsUrl, type DocPage } from './docsUrl'

test('each product help link opens a section of the bundled guide', () => {
  const guide = readFileSync(new URL('../../public/product-guide.html', import.meta.url), 'utf8')
  for (const page of Object.keys(DOC_PAGES) as DocPage[]) {
    const url = new URL(docsUrl(page), 'https://product.example')
    assert.equal(url.origin, 'https://product.example')
    assert.equal(url.pathname, '/product-guide.html')
    if (url.hash) assert.ok(guide.includes('id="' + url.hash.slice(1) + '"'), page)
  }
})
