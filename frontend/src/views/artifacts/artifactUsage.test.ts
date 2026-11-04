import assert from 'node:assert/strict'
import { test } from 'node:test'
import { parseArtifactUsage } from './artifactUsage'

test('accepts an exact full-scope aggregate including zero', () => {
  const data = { file_count: 0, logical_bytes: 0, scope: 'accessible_artifact_versions' as const }
  assert.deepEqual(parseArtifactUsage({ success: true, data }), data)
})

test('reports unknown for failed, malformed, or differently scoped aggregates', () => {
  const data = { file_count: 2, logical_bytes: 123, scope: 'accessible_artifact_versions' as const }
  assert.equal(parseArtifactUsage(null), null)
  assert.equal(parseArtifactUsage({ success: false, data }), null)
  assert.equal(parseArtifactUsage({ success: true, data: { ...data, file_count: -1 } }), null)
  assert.equal(parseArtifactUsage({ success: true, data: { ...data, logical_bytes: NaN } }), null)
  assert.equal(parseArtifactUsage({ success: true, data: { ...data, scope: 'other' as typeof data.scope } }), null)
})
