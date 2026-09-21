import type { GitHubBatchResultItem, GitHubRepository } from '@/api/datasource'

/**
 * Keeps the bulk-import drawer's selection rules independent from its Vue
 * surface. Repositories are identified by their canonical owner/name string,
 * so a repeated API page cannot create duplicate source rows.
 */
export function uniqueGitHubRepositories(repositories: GitHubRepository[]): GitHubRepository[] {
  const seen = new Set<string>()
  return repositories.filter((repository) => {
    const key = repository.repository.trim().toLowerCase()
    if (!key || seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export function defaultGitHubBulkSelection(repositories: GitHubRepository[]): string[] {
  // Discovery is deliberately non-mutating. Requiring an explicit selection
  // avoids silently creating many data sources when an organization has a large
  // repository list.
  void repositories
  return []
}

export function selectableGitHubRepository(repository: GitHubRepository): boolean {
  return !repository.archived && !repository.disabled && !repository.fork
}

export function filterGitHubRepositories(
  repositories: GitHubRepository[],
  query: string,
  includeArchived: boolean,
): GitHubRepository[] {
  const needle = query.trim().toLowerCase()
  return uniqueGitHubRepositories(repositories).filter((repository) => {
    if (!includeArchived && (repository.archived || repository.disabled || repository.fork)) return false
    if (!needle) return true
    return [repository.repository, repository.description, repository.default_branch]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(needle))
  })
}

export function parseGitHubPaths(value: string): string[] {
  return Array.from(new Set(
    value
      .split(/\r?\n/)
      .map((path) => path.trim())
      .filter(Boolean),
  ))
}

export type GitHubBulkResultSummary = {
  created: number
  existing: number
  failed: number
  other: number
}

export function summarizeGitHubBulkResults(results: GitHubBatchResultItem[]): GitHubBulkResultSummary {
  return results.reduce<GitHubBulkResultSummary>((summary, result) => {
    if (result.status === 'created') summary.created += 1
    else if (result.status === 'existing') summary.existing += 1
    else if (result.status === 'failed') summary.failed += 1
    else summary.other += 1
    return summary
  }, { created: 0, existing: 0, failed: 0, other: 0 })
}
