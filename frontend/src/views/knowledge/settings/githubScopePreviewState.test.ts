import assert from 'node:assert/strict'
import test from 'node:test'
import {
  canShowGitHubScopePreview,
  githubScopePreviewErrorKey,
  githubScopePreviewRequest,
} from './githubScopePreviewState'

test('preview request forwards unsaved paths and optional exclusions without mutating form settings', () => {
  const settings = { paths: ['README.md', 'docs'], exclude: ['docs/drafts'] }
  assert.deepEqual(githubScopePreviewRequest('source-a', settings), {
    source_id: 'source-a', paths: ['README.md', 'docs'], exclude: ['docs/drafts'],
  })
  assert.deepEqual(settings, { paths: ['README.md', 'docs'], exclude: ['docs/drafts'] })
  assert.deepEqual(githubScopePreviewRequest('source-a', { paths: null }), {
    source_id: 'source-a', paths: [],
  })
})

test('only complete and clearly partial trees can display count cards', () => {
  assert.equal(canShowGitHubScopePreview({ tree_state: 'complete' }), true)
  assert.equal(canShowGitHubScopePreview({ tree_state: 'truncated' }), true)
  assert.equal(canShowGitHubScopePreview({ tree_state: 'missing_path' }), false)
  assert.equal(canShowGitHubScopePreview({ tree_state: 'error' }), false)
  assert.equal(githubScopePreviewErrorKey({ tree_state: 'missing_path' }), 'datasource.githubBulk.scopePreview.missingPath')
  assert.equal(githubScopePreviewErrorKey({ tree_state: 'error', error_code: 'github_rate_limit' }), 'datasource.githubBulk.scopePreview.failed')
  assert.equal(githubScopePreviewErrorKey({ tree_state: 'error', error_code: 'github_exclusion_invalid' }), 'datasource.githubBulk.scopePreview.invalidExclude')
})
