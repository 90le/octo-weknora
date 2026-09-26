import assert from 'node:assert/strict'
import test from 'node:test'

import type { GitHubBatchScopePreview, GitHubRepository } from '@/api/datasource'
import {
  effectiveGitHubBulkScope, githubBulkReviewKey, isGitHubPreviewRateLimit, mapGitHubPreviewsBounded, validGitHubBulkReview,
} from './githubBulkScopeReviewState'

const repository: GitHubRepository = { repository: 'Example/Repo', default_branch: 'main', archived: false, description: '' }
const preview = (eligible: number, expires: string): GitHubBatchScopePreview => ({
  repository: 'example/repo', ref: 'main', mode: 'documents', commit: 'a'.repeat(40), tree_state: 'complete',
  paths: ['docs'], exclude: [], estimated: false, warnings: [], preview_token: 'signed', expires_at: expires,
  summary: { candidate_files: eligible, candidate_bytes: 100, eligible_files: eligible, eligible_bytes: 100,
    image_files: 0, image_bytes: 0, sensitive_candidate_files: 0, sensitive_candidate_bytes: 0,
    user_excluded_files: 0, user_excluded_bytes: 0,
    parser_unsupported_files: 0, parser_unsupported_bytes: 0, too_large_files: 0, too_large_bytes: 0,
    extensions: [], top_directories: [], sample_paths: [], warnings: [] },
})

test('effective batch selection preserves global defaults and explicit per-repo empty exclusion', () => {
  const inherited = effectiveGitHubBulkScope('README.md', '')
  assert.deepEqual(inherited.paths, ['README.md'])
  assert.equal(inherited.exclude, undefined)
  assert.equal(inherited.candidateExclude, undefined)
  const overridden = effectiveGitHubBulkScope('README.md', 'dist', {
    pathsEnabled: true, pathsText: 'docs\nREADME.md', excludeEnabled: true, excludeText: '',
  })
  assert.deepEqual(overridden.paths, ['docs', 'README.md'])
  assert.deepEqual(overridden.exclude, [])
  assert.deepEqual(overridden.candidateExclude, [])
  assert.notEqual(githubBulkReviewKey(repository, 'documents', inherited, 0),
    githubBulkReviewKey(repository, 'documents', overridden, 0))
})

test('creation requires current complete unexpired preview and explicit empty confirmation', () => {
  const scope = effectiveGitHubBulkScope('docs', '')
  const key = githubBulkReviewKey(repository, 'documents', scope, 2)
  const now = Date.parse('2026-09-27T10:00:00Z')
  const expires = new Date(now + 60_000).toISOString()
  assert.equal(validGitHubBulkReview({ key, preview: preview(1, expires), allowEmpty: false }, key, now), true)
  assert.equal(validGitHubBulkReview({ key, preview: preview(0, expires), allowEmpty: false }, key, now), false)
  assert.equal(validGitHubBulkReview({ key, preview: preview(0, expires), allowEmpty: true }, key, now), true)
  assert.equal(validGitHubBulkReview({ key, preview: preview(1, expires), allowEmpty: false }, key + '-changed', now), false)
  assert.equal(validGitHubBulkReview({ key, preview: preview(1, expires), allowEmpty: false }, key, now + 61_000), false)
  const failed = preview(1, expires)
  failed.error_code = 'github_preview_unavailable'
  assert.equal(validGitHubBulkReview({ key, preview: failed, allowEmpty: false }, key, now), false)
})

test('explicit many-repository preview keeps concurrency at two and preserves order', async () => {
  let inFlight = 0
  let peak = 0
  const values = await mapGitHubPreviewsBounded(Array.from({ length: 30 }, (_, index) => index), async (value) => {
    inFlight += 1
    peak = Math.max(peak, inFlight)
    await new Promise(resolve => setTimeout(resolve, value % 3))
    inFlight -= 1
    return value * 2
  })
  assert.equal(peak, 2)
  assert.deepEqual(values, Array.from({ length: 30 }, (_, index) => index * 2))
  await assert.rejects(mapGitHubPreviewsBounded([1], async value => value, 3), /1 or 2/)
})

test('a rate-limited first wave defers every later repository without another request', async () => {
  const requested: number[] = []
  const results = await mapGitHubPreviewsBounded(Array.from({ length: 30 }, (_, index) => index), async value => {
    requested.push(value)
    await new Promise(resolve => setTimeout(resolve, value === 0 ? 2 : 1))
    return value === 0 ? 'rate_limited' : 'ready'
  }, 2, result => result === 'rate_limited')
  assert.deepEqual(requested, [0, 1])
  assert.deepEqual(results.slice(0, 2), ['rate_limited', 'ready'])
  assert.equal(results.slice(2).every(result => result === undefined), true)
  assert.equal(isGitHubPreviewRateLimit('github_rate_limit'), true)
  assert.equal(isGitHubPreviewRateLimit('github_secondary_rate_limit'), true)
  assert.equal(isGitHubPreviewRateLimit(undefined, 429), true)
  assert.equal(isGitHubPreviewRateLimit('github_auth', 403), false)
})
