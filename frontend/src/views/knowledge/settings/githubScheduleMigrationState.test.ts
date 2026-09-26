import assert from 'node:assert/strict'
import test from 'node:test'

import type { GitHubScheduleMigrationPreviewItem } from '@/api/datasource'
import { selectedGitHubScheduleMigrations } from './githubScheduleMigrationState'

const item = (id: string, eligible: boolean): GitHubScheduleMigrationPreviewItem => ({
  data_source_id: id,
  repository: `owner/${id}`,
  status: eligible ? 'active' : 'paused',
  current_schedule: '0 0 */6 * * *',
  proposed_schedule: eligible ? '0 15 2,8,14,20 * * *' : '',
  updated_at: '2026-09-27T10:00:00Z',
  eligible,
  reason: eligible ? 'eligible' : 'not_active',
})

test('schedule migration sends only selected eligible preview rows with version and server proposal', () => {
  const choices = selectedGitHubScheduleMigrations([item('source-a', true), item('paused', false), item('source-b', true)],
    ['source-a', 'paused', 'outside-kb'])
  assert.deepEqual(choices, [{
    data_source_id: 'source-a',
    expected_schedule: '0 0 */6 * * *',
    expected_status: 'active',
    expected_updated_at: '2026-09-27T10:00:00Z',
    expected_proposed: '0 15 2,8,14,20 * * *',
  }])
})
