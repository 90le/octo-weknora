import assert from 'node:assert/strict'
import test from 'node:test'

import {
  addGitHubRepositoryModePresence,
  defaultGitHubBulkSelection,
  filterGitHubRepositories,
  githubRepositoryModePresence,
  githubBatchSyncPayload,
  hasGitHubRepositoryMode,
  mergeGitHubRepositoryPresence,
  normalizeGitHubRepository,
  parseGitHubPaths,
  selectableGitHubRepository,
  selectableGitHubRepositoryMode,
  summarizeGitHubBulkResults,
} from './githubBulkImportState'
import type { DataSource } from '@/api/datasource'

const repositories = [
  { repository: 'Mininglamp-OSS/octo-cli', default_branch: 'main', archived: false, description: 'CLI' },
  { repository: 'Mininglamp-OSS/octo-server', default_branch: 'main', archived: true, description: 'Server' },
  { repository: 'Mininglamp-OSS/OCTO-CLI', default_branch: 'main', archived: false, description: 'Duplicate' },
]

test('GitHub bulk discovery starts with an explicit empty selection', () => {
  assert.deepEqual(defaultGitHubBulkSelection(repositories), [])
  assert.equal(selectableGitHubRepository(repositories[0]), true)
  assert.equal(selectableGitHubRepository(repositories[1]), false)
})

test('GitHub bulk filters search across repository metadata without selecting archived by surprise', () => {
  assert.deepEqual(
    filterGitHubRepositories(repositories, 'server', false),
    [],
  )
  assert.deepEqual(
    filterGitHubRepositories(repositories, 'server', true).map((repository) => repository.repository),
    ['Mininglamp-OSS/octo-server'],
  )
})

test('GitHub bulk paths and result summaries remain deterministic', () => {
  assert.deepEqual(parseGitHubPaths('README.md\n docs \nREADME.md\n'), ['README.md', 'docs'])
  assert.deepEqual(
    summarizeGitHubBulkResults([
      { repository: 'a/one', status: 'created' },
      { repository: 'a/two', status: 'existing' },
      { repository: 'a/three', status: 'failed' },
      { repository: 'a/four', status: 'skipped' },
    ]),
    { created: 1, existing: 1, failed: 1, other: 1 },
  )
})

test('GitHub bulk treats no schedule as an explicit dormant manual source', () => {
  assert.deepEqual(githubBatchSyncPayload('', true), {
    sync_policy: 'manual',
    start_sync: false,
  })
  assert.deepEqual(githubBatchSyncPayload('  ', false), {
    sync_policy: 'manual',
    start_sync: false,
  })
  assert.deepEqual(githubBatchSyncPayload(' 0 0 */6 * * * ', true), {
    sync_policy: 'scheduled',
    sync_schedule: '0 0 */6 * * *',
    start_sync: true,
  })
})

test('GitHub bulk tracks source and document ingestion independently', () => {
  const dataSources = [
    {
      id: 'source-1', type: 'github', config: { settings: { repository: 'https://github.com/Mininglamp-OSS/octo-cli.git', mode: 'source' } },
    },
    {
      id: 'documents-1', type: 'github', config: { settings: { repository: 'git@github.com:Mininglamp-OSS/octo-server.git' } },
    },
    { id: 'not-github', type: 'local_folder', config: { settings: { repository: 'Mininglamp-OSS/octo-cli', mode: 'documents' } } },
  ] as DataSource[]

  const presence = githubRepositoryModePresence(dataSources)
  assert.equal(normalizeGitHubRepository('Mininglamp-OSS/octo-cli.git'), 'mininglamp-oss/octo-cli')
  assert.equal(normalizeGitHubRepository('https://github.com/MININGLAMP-OSS/OCTO-CLI'), 'mininglamp-oss/octo-cli')
  assert.equal(normalizeGitHubRepository('Mininglamp-OSS/octo-cli/tree/main'), '')
  assert.equal(normalizeGitHubRepository('Mininglamp-OSS/octo-cli/blob/main/README.md'), '')
  assert.equal(hasGitHubRepositoryMode('Mininglamp-OSS/octo-cli', 'source', presence), true)
  assert.equal(hasGitHubRepositoryMode('Mininglamp-OSS/octo-cli', 'documents', presence), false)
  // A missing legacy mode is document ingestion, not a source snapshot.
  assert.equal(hasGitHubRepositoryMode('Mininglamp-OSS/octo-server', 'documents', presence), true)

  // A repo already configured for source is still selectable for document
  // ingestion, and vice versa. Only the exact repository/mode pair is locked.
  assert.equal(selectableGitHubRepositoryMode(repositories[0], 'source', presence), false)
  assert.equal(selectableGitHubRepositoryMode(repositories[0], 'documents', presence), true)
})

test('GitHub bulk immediately marks successful result modes without waiting for a list refresh', () => {
  const added = addGitHubRepositoryModePresence({}, 'Mininglamp-OSS/octo-cli', 'documents')
  const merged = mergeGitHubRepositoryPresence({
    'mininglamp-oss/octo-cli': { source: true, documents: false },
  }, added)

  assert.deepEqual(merged['mininglamp-oss/octo-cli'], { source: true, documents: true })
})
