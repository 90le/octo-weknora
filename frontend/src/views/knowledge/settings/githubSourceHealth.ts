/** Read-only card state. Transport connection (`source.status`) and content
 * completeness (`latest_sync_log`) are separate facts: an active source can
 * have a failed or partial latest attempt. Never infer a full success from a
 * last_sync_at value, which is also advanced by partial runs. */
export interface GitHubHealthSource {
  type: string
  status: string
  sync_schedule?: string
  last_sync_at?: string | null
  last_successful_sync_at?: string | null
  last_sync_result?: { failed?: number; total?: number; retry_not_before?: string } | null
  latest_sync_summary?: {
    status: string
    items_total?: number
    items_failed?: number
    started_at?: string
    finished_at?: string | null
  } | null
  latest_sync_log?: {
    status: string
    items_total?: number
    items_failed?: number
    started_at?: string
    finished_at?: string | null
  } | null
}

export type GitHubHealthKind = 'running' | 'error' | 'paused' | 'partial' | 'failed_attempt'
  | 'cooldown' | 'healthy' | 'stale' | 'unknown'

export interface GitHubHealthView {
  kind: GitHubHealthKind
  theme: 'success' | 'warning' | 'danger' | 'primary' | 'default'
  failed: number
  total: number
  attemptAt: string | null
  successAt: string | null
  retryAt: string | null
}

function timestamp(value?: string | null): number | null {
  if (!value) return null
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? parsed : null
}

function nonNegativeCount(value: unknown): number {
  const count = Number(value)
  return Number.isFinite(count) ? Math.max(0, count) : 0
}

function expectedIntervalMs(cron?: string): number | null {
  if (!cron) return null
  if (cron === '0 0 * * * *') return 60 * 60_000
  if (cron === '0 0 */6 * * *') return 6 * 60 * 60_000
  if (cron === '0 0 */12 * * *') return 12 * 60 * 60_000
  if (cron === '0 0 2 * * *') return 24 * 60 * 60_000
  if (cron === '0 0 0 * * *') return 24 * 60 * 60_000
  if (cron === '0 0 0 * * 1') return 7 * 24 * 60 * 60_000
  const match = /^0 \d{1,2} (\d{1,2}),(\d{1,2}),(\d{1,2}),(\d{1,2}) \* \* \*$/.exec(cron)
  if (!match) return null
  const hours = match.slice(1).map(Number)
  return hours.every((hour, index) => hour < 24 && hour === hours[0] + index * 6)
    ? 6 * 60 * 60_000 : null
}

export function githubSourceHealth(source: GitHubHealthSource, now = Date.now()): GitHubHealthView | null {
  if (source.type !== 'github') return null
  const log = source.latest_sync_summary || source.latest_sync_log
  const attemptAt = log?.finished_at || log?.started_at || source.last_sync_at || null
  const successAt = source.last_successful_sync_at || (log?.status === 'success' ? log.finished_at || null : null)
  const retryAt = source.last_sync_result?.retry_not_before || null
  const failed = nonNegativeCount(log ? log.items_failed : source.last_sync_result?.failed)
  const total = nonNegativeCount(log ? log.items_total : source.last_sync_result?.total)
  const view = (kind: GitHubHealthKind, theme: GitHubHealthView['theme']): GitHubHealthView => ({
    kind, theme, failed, total, attemptAt, successAt, retryAt,
  })
  if (log?.status === 'running') {
    const started = timestamp(log.started_at)
    return started !== null && now - started > 2 * 60 * 60_000 + 5 * 60_000
      ? view('stale', 'warning') : view('running', 'primary')
  }
  if (source.status === 'error') return view('error', 'danger')
  if (source.status === 'paused') return view('paused', 'default')
  const retry = timestamp(retryAt)
  if (retry !== null && retry > now) return view('cooldown', 'warning')
  if (log?.status === 'partial') return view('partial', 'warning')
  if (log?.status === 'failed') return view('failed_attempt', 'warning')
  const interval = expectedIntervalMs(source.sync_schedule)
  const attempt = timestamp(attemptAt)
  if (interval && attempt !== null && now - attempt > 2 * interval + 60 * 60_000) {
    return view('stale', 'warning')
  }
  if (log?.status === 'success') return view('healthy', 'success')
  return view('unknown', 'default')
}
