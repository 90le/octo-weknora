import type { GitHubBatchScopePreview, GitHubBulkMode, GitHubRepository } from '@/api/datasource'
import { normalizeGitHubRepository, parseGitHubPaths } from './githubBulkImportState'

export interface GitHubBulkRowOverride {
  pathsEnabled: boolean
  pathsText: string
  excludeEnabled: boolean
  excludeText: string
}

export interface GitHubBulkEffectiveScope {
  paths: string[]
  /** Undefined inherits the source default; [] explicitly clears it. */
  exclude?: string[]
  candidatePaths?: string[]
  candidateExclude?: string[]
}

export interface GitHubBulkReview {
  key: string
  preview?: GitHubBatchScopePreview
  error?: string
  allowEmpty: boolean
}

export function effectiveGitHubBulkScope(
  globalPathsText: string, globalExcludeText: string, override?: GitHubBulkRowOverride,
): GitHubBulkEffectiveScope {
  const globalPaths = parseGitHubPaths(globalPathsText)
  const globalExclude = parseGitHubPaths(globalExcludeText)
  return {
    paths: override?.pathsEnabled ? parseGitHubPaths(override.pathsText) : globalPaths,
    exclude: override?.excludeEnabled ? parseGitHubPaths(override.excludeText)
      : globalExclude.length ? globalExclude : undefined,
    candidatePaths: override?.pathsEnabled ? parseGitHubPaths(override.pathsText) : undefined,
    candidateExclude: override?.excludeEnabled ? parseGitHubPaths(override.excludeText) : undefined,
  }
}

export function githubBulkReviewKey(
  repository: GitHubRepository, mode: GitHubBulkMode, scope: GitHubBulkEffectiveScope, credentialRevision: number,
): string {
  return JSON.stringify([normalizeGitHubRepository(repository.repository), repository.default_branch.trim(), mode,
    scope.paths, scope.exclude === undefined ? null : scope.exclude, credentialRevision])
}

export function validGitHubBulkReview(review: GitHubBulkReview | undefined, key: string, now: number): boolean {
  if (!review || review.key !== key || !review.preview?.preview_token || review.preview.error_code || review.preview.tree_state !== 'complete') return false
  const expires = Date.parse(review.preview.expires_at || '')
  if (!Number.isFinite(expires) || expires <= now) return false
  return review.preview.summary.eligible_files > 0 || review.allowEmpty
}

// Explicit batch preview may contain many selected rows, but never opens more
// than two GitHub tree requests at once. A worker handles one repository per
// call, preserving per-row success/error results and original selection order.
export async function mapGitHubPreviewsBounded<T, R>(
  items: readonly T[], worker: (item: T) => Promise<R>, concurrency = 2,
): Promise<R[]> {
  if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 2) {
    throw new RangeError('GitHub preview concurrency must be 1 or 2')
  }
  const results = new Array<R>(items.length)
  let next = 0
  await Promise.all(Array.from({ length: Math.min(items.length, concurrency) }, async () => {
    while (next < items.length) {
      const index = next++
      results[index] = await worker(items[index])
    }
  }))
  return results
}
