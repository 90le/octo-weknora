import assert from 'node:assert/strict'
import test from 'node:test'

import {
  defaultGitHubBulkSelection,
  filterGitHubRepositories,
  parseGitHubPaths,
  selectableGitHubRepository,
  summarizeGitHubBulkResults,
} from './githubBulkImportState'

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
