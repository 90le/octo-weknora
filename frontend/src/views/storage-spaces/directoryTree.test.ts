import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ancestorDirectories, immediateDirectories, isRelativeDirectory, nextDirectoryOffset } from './directoryTree.ts'

test('directory tree keeps only immediate, safe and unique children supplied by the server', () => {
  const children = [
    {name:'说明', directory:'products/说明'},
    {name:'duplicate', directory:'products/说明'},
    {name:'parent', directory:'products/..'},
    {name:'neighbor', directory:'products-old/docs'},
    {name:'deep', directory:'products/a/b'},
    {name:'absolute', directory:'/etc'},
    {name:'backslash', directory:'products/a\\b'},
    {name:'empty', directory:'products/'},
  ]
  assert.deepEqual(immediateDirectories('products', children), [{name:'说明', directory:'products/说明'}])
  assert.deepEqual(immediateDirectories('../', children), [])
  assert.deepEqual(immediateDirectories('', [{name:'top',directory:'top'}, {name:'deep',directory:'top/a'}]), [{name:'top',directory:'top'}])
})

test('restoring a nested selection loads only safe ancestor directories in order', () => {
  assert.deepEqual(ancestorDirectories('products/AIBP/使用说明'), ['', 'products', 'products/AIBP'])
  assert.deepEqual(ancestorDirectories(''), [''])
  for (const path of ['/etc','../secrets','a/../b','a/./b','a//b','a\\b','a\0b']) {
    assert.equal(isRelativeDirectory(path), false, path)
    assert.deepEqual(ancestorDirectories(path), [''])
  }
})

test('directory pagination cannot loop or skip to invalid offsets', () => {
  const page = {directory:'', entries:[], has_more:true, next_offset:100, truncated:false}
  assert.equal(nextDirectoryOffset(page, 0), 100)
  for (const next of [0, -1, 1.5, NaN, Infinity]) assert.equal(nextDirectoryOffset({...page,next_offset:next},0),null)
  assert.equal(nextDirectoryOffset(page,100),null)
  assert.equal(nextDirectoryOffset({...page,has_more:false},0),null)
})
