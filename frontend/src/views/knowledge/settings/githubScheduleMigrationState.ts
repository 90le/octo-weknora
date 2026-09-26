import type { GitHubScheduleMigrationPreviewItem, GitHubScheduleMigrationSelection } from '@/api/datasource'

/** Build an apply request only from eligible rows the manager selected in the
 * current server preview. A stale checkbox ID cannot introduce another KB's
 * source or replace the server-computed target cron. */
export function selectedGitHubScheduleMigrations(
  items: readonly GitHubScheduleMigrationPreviewItem[], selectedIDs: readonly string[],
): GitHubScheduleMigrationSelection[] {
  const selected = new Set(selectedIDs)
  return items.filter(item => item.eligible && selected.has(item.data_source_id) && Boolean(item.proposed_schedule))
    .map(item => ({
      data_source_id: item.data_source_id,
      expected_schedule: item.current_schedule,
      expected_status: item.status,
      expected_updated_at: item.updated_at,
      expected_proposed: item.proposed_schedule || '',
    }))
}
