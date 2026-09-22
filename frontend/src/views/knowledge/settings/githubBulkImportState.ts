import type {
  DataSource,
  GitHubBatchResultItem,
  GitHubBatchSyncPolicy,
  GitHubBulkMode,
  GitHubRepository,
} from '@/api/datasource'

/**
 * Whether a repository already has either of the two deliberately independent
 * GitHub usages in the current knowledge base. `source` is the read-only
 * source snapshot; `documents` goes through the normal parser/RAG pipeline.
 */
export type GitHubRepositoryModePresence = Record<GitHubBulkMode, boolean>
export type GitHubRepositoryPresenceMap = Record<string, GitHubRepositoryModePresence>

const emptyModePresence = (): GitHubRepositoryModePresence => ({ source: false, documents: false })

/**
 * GitHub treats owner/repository names case-insensitively. Existing normal
 * data sources may contain a GitHub URL or an SSH remote from older forms, so
 * normalize those shapes before comparing them with discovery results.
 */
export function normalizeGitHubRepository(repository: unknown): string {
  if (typeof repository !== 'string') return ''
  let value = repository.trim()
    .replace(/^https?:\/\/(?:www\.)?github\.com\//i, '')
    .replace(/^git@github\.com[:/]/i, '')
    .replace(/^ssh:\/\/git@github\.com\//i, '')
    .replace(/^\/+|\/+$/g, '')
    .replace(/\.git$/i, '')

  const parts = value.split('/').filter(Boolean)
  // Do not collapse a branch/file URL such as owner/repo/tree/main into the
  // repository key. It is not a repository identity and the backend rejects
  // it too; treating it as one here would incorrectly hide a valid picker row.
  if (parts.length !== 2) return ''
  value = `${parts[0]}/${parts[1]}`
  return value.toLowerCase()
}

function dataSourceSettings(dataSource: DataSource): Record<string, unknown> | null {
  let config = dataSource.config
  if (typeof config === 'string') {
    try {
      config = JSON.parse(config)
    } catch {
      return null
    }
  }
  const settings = config?.settings
  return settings && typeof settings === 'object' && !Array.isArray(settings)
    ? settings as Record<string, unknown>
    : null
}

/** Builds a canonical repository -> mode presence map from the normal source list. */
export function githubRepositoryModePresence(dataSources: DataSource[]): GitHubRepositoryPresenceMap {
  const presence: GitHubRepositoryPresenceMap = {}
  for (const dataSource of dataSources) {
    if (dataSource.type !== 'github') continue
    const settings = dataSourceSettings(dataSource)
    const repository = normalizeGitHubRepository(settings?.repository)
    if (!repository) continue

    // Earlier single-source GitHub configurations did not store mode. They
    // always used document ingestion, so represent them accurately instead
    // of offering a duplicate document source in the batch picker.
    const mode: GitHubBulkMode = settings?.mode === 'source' ? 'source' : 'documents'
    const current = presence[repository] || emptyModePresence()
    current[mode] = true
    presence[repository] = current
  }
  return presence
}

export function mergeGitHubRepositoryPresence(
  base: GitHubRepositoryPresenceMap,
  additions: GitHubRepositoryPresenceMap,
): GitHubRepositoryPresenceMap {
  const merged: GitHubRepositoryPresenceMap = {}
  for (const [repository, modes] of Object.entries(base)) {
    merged[repository] = { ...emptyModePresence(), ...modes }
  }
  for (const [repository, modes] of Object.entries(additions)) {
    const current = merged[repository] || emptyModePresence()
    // Presence is monotonic while the drawer is open: a successful result may
    // add one mode but must never erase the other mode learned from the list.
    merged[repository] = {
      source: current.source || modes.source,
      documents: current.documents || modes.documents,
    }
  }
  return merged
}

export function addGitHubRepositoryModePresence(
  presence: GitHubRepositoryPresenceMap,
  repository: string,
  mode: GitHubBulkMode,
): GitHubRepositoryPresenceMap {
  const key = normalizeGitHubRepository(repository)
  if (!key) return presence
  return {
    ...presence,
    [key]: { ...emptyModePresence(), ...presence[key], [mode]: true },
  }
}

export function hasGitHubRepositoryMode(
  repository: string,
  mode: GitHubBulkMode,
  presence: GitHubRepositoryPresenceMap,
): boolean {
  const key = normalizeGitHubRepository(repository)
  return Boolean(key && presence[key]?.[mode])
}

/** A repository is selectable when it is valid and missing only the mode being added. */
export function selectableGitHubRepositoryMode(
  repository: GitHubRepository,
  mode: GitHubBulkMode,
  presence: GitHubRepositoryPresenceMap,
): boolean {
  return selectableGitHubRepository(repository)
    && !hasGitHubRepositoryMode(repository.repository, mode, presence)
}

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

export type GitHubBatchSyncPayload = {
  sync_policy: GitHubBatchSyncPolicy
  sync_schedule?: string
  start_sync: boolean
}

/**
 * The blank schedule choice is an explicit manual policy, not an omitted
 * value. That prevents the backend's legacy default schedule from turning a
 * deliberately dormant source into a six-hour sync job. Manual sources are
 * created only; users start their first sync from the normal source card.
 */
export function githubBatchSyncPayload(schedule: string, startSync: boolean): GitHubBatchSyncPayload {
  const normalizedSchedule = schedule.trim()
  if (!normalizedSchedule) {
    return { sync_policy: 'manual', start_sync: false }
  }
  return {
    sync_policy: 'scheduled',
    sync_schedule: normalizedSchedule,
    start_sync: startSync,
  }
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
