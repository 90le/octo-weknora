import assert from 'node:assert/strict'
import test from 'node:test'

import { githubSourceHealth, type GitHubHealthSource } from './githubSourceHealth'

const now = Date.parse('2026-09-27T12:00:00Z')
const hoursAgo = (hours: number) => new Date(now - hours * 60 * 60_000).toISOString()
const base = (overrides: Partial<GitHubHealthSource> = {}): GitHubHealthSource => ({
  type: 'github', status: 'active', sync_schedule: '0 0 */6 * * *',
  last_sync_at: hoursAgo(1), last_successful_sync_at: hoursAgo(1),
  latest_sync_log: { status: 'success', started_at: hoursAgo(1), finished_at: hoursAgo(1), items_total: 10, items_failed: 0 },
  ...overrides,
})

test('five error and nine active-partial GitHub sources never render as healthy', () => {
  const sources = [
    ...Array.from({ length: 5 }, () => base({
      status: 'error', last_successful_sync_at: hoursAgo(25),
      latest_sync_log: { status: 'failed', started_at: hoursAgo(1), items_total: 0, items_failed: 0 },
    })),
    ...Array.from({ length: 9 }, () => base({
      status: 'active', last_successful_sync_at: hoursAgo(25),
      latest_sync_log: { status: 'partial', started_at: hoursAgo(1), finished_at: hoursAgo(1), items_total: 500, items_failed: 212 },
    })),
  ]
  const views = sources.map(source => githubSourceHealth(source, now))
  assert.equal(views.filter(view => view?.kind === 'error').length, 5)
  assert.equal(views.filter(view => view?.kind === 'partial').length, 9)
  assert.equal(views.filter(view => view?.kind === 'healthy').length, 0)
  assert.equal(views[5]?.failed, 212)
  assert.equal(views[5]?.total, 500)
  assert.equal(views[5]?.successAt, hoursAgo(25))
})

test('running, complete, overdue and unknown states use attempt evidence, not connection status', () => {
  assert.equal(githubSourceHealth(base({ latest_sync_log: { status: 'running', started_at: hoursAgo(1) } }), now)?.kind, 'running')
  assert.equal(githubSourceHealth(base({ latest_sync_log: { status: 'running', started_at: hoursAgo(3) } }), now)?.kind, 'stale')
  assert.equal(githubSourceHealth(base(), now)?.kind, 'healthy')
  assert.equal(githubSourceHealth(base({
    last_sync_at: hoursAgo(20), last_successful_sync_at: hoursAgo(20),
    latest_sync_log: { status: 'success', started_at: hoursAgo(20), finished_at: hoursAgo(20) },
  }), now)?.kind, 'stale')
  assert.equal(githubSourceHealth(base({
    latest_sync_log: undefined, last_sync_at: hoursAgo(2), last_successful_sync_at: null,
    last_sync_result: { failed: 3, total: 5 },
  }), now)?.kind, 'unknown')
  assert.equal(githubSourceHealth({ ...base(), type: 'gitlab' }, now), null)
})

test('active failed attempt and durable rate-limit cooldown remain visible', () => {
  const failed = base({ latest_sync_log: { status: 'failed', started_at: hoursAgo(1), finished_at: hoursAgo(1), items_total: 3, items_failed: 3 } })
  assert.equal(githubSourceHealth(failed, now)?.kind, 'failed_attempt')
  const retryAt = new Date(now + 6 * 60 * 60_000).toISOString()
  assert.equal(githubSourceHealth({ ...failed, last_sync_result: { retry_not_before: retryAt } }, now)?.kind, 'cooldown')
  assert.equal(githubSourceHealth({ ...failed, status: 'paused' }, now)?.kind, 'paused')
  const completeStaggered = base({ sync_schedule: '0 15 2,8,14,20 * * *' })
  assert.equal(githubSourceHealth(completeStaggered, now)?.kind, 'healthy')
  assert.equal(githubSourceHealth({ ...completeStaggered,
    latest_sync_log: { status: 'success', started_at: hoursAgo(20), finished_at: hoursAgo(20) },
  }, now)?.kind, 'stale')
  // New bounded API summary is authoritative for the card; the old full log
  // remains in the response only for compatibility with existing clients.
  assert.equal(githubSourceHealth({ ...base(),
    latest_sync_summary: { status: 'partial', items_total: 20, items_failed: 4, started_at: hoursAgo(1) },
  }, now)?.kind, 'partial')
})
