import type {
  DataSource,
  DataSourceRestartRecoveryCandidate,
  DataSourceRestartRecoveryPreview,
  DataSourceRestartRecoveryRunStatus,
} from '@/api/datasource'

/** Recovery is a manager action for GitHub document ingestion only. */
export function canPreviewRestartRecoveryForSource(dataSource: Pick<DataSource, 'type' | 'config'>, isAdmin: boolean): boolean {
  return isAdmin && dataSource.type === 'github' && dataSource.config?.settings?.mode !== 'source'
}

export function isRestartRecoveryTerminal(status?: DataSourceRestartRecoveryRunStatus): boolean {
  return status === 'completed' || status === 'partial' || status === 'failed' || status === 'blocked'
}

export function canStartRestartRecovery(
  preview: DataSourceRestartRecoveryPreview | null,
  loadingPreview: boolean,
  starting: boolean,
): boolean {
  return Boolean(
    preview?.preview_token && preview.eligible_count > 0 &&
    !(preview.blockers?.length) && !loadingPreview && !starting,
  )
}

/** Counts display rows only; the opaque executable plan remains server-owned. */
export function summarizeRestartRecoveryCandidates(candidates: DataSourceRestartRecoveryCandidate[]) {
  return candidates.reduce((summary, candidate) => {
    if (candidate.state === 'pending') summary.eligible += 1
    else if (candidate.state === 'blocked') summary.blocked += 1
    else summary.excluded += 1
    return summary
  }, { eligible: 0, excluded: 0, blocked: 0 })
}
