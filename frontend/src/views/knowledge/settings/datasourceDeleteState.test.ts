import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DEFAULT_DATASOURCE_DELETE_MODE,
  formatGeneratedStorageBytes,
  hasLegacyUnverifiableResources,
  hasRetainedDeleteResources,
} from './datasourceDeleteState'

test('data-source removal starts in detach mode and formats preview storage safely', () => {
  assert.equal(DEFAULT_DATASOURCE_DELETE_MODE, 'detach')
  assert.equal(formatGeneratedStorageBytes(0, 'en-US'), '0 B')
  assert.equal(formatGeneratedStorageBytes(1536, 'en-US'), '1.5 KB')
  assert.equal(formatGeneratedStorageBytes(12 * 1024 * 1024, 'en-US'), '12 MB')
  assert.equal(formatGeneratedStorageBytes(Number.NaN, 'en-US'), '0 B')
})

test('only a positive shared-or-unverifiable count is presented as retained', () => {
  assert.equal(hasRetainedDeleteResources(0), false)
  assert.equal(hasRetainedDeleteResources(-1), false)
  assert.equal(hasRetainedDeleteResources(2), true)
  assert.equal(hasRetainedDeleteResources(Number.NaN), false)
})

test('only a positive legacy count blocks generated-content cleanup', () => {
  assert.equal(hasLegacyUnverifiableResources(0), false)
  assert.equal(hasLegacyUnverifiableResources(-1), false)
  assert.equal(hasLegacyUnverifiableResources(1), true)
  assert.equal(hasLegacyUnverifiableResources(Number.NaN), false)
})
