import type { DataSourceDeleteMode } from '@/api/datasource'

/** The safe default: stop syncing without deleting already indexed knowledge. */
export const DEFAULT_DATASOURCE_DELETE_MODE: DataSourceDeleteMode = 'detach'

/**
 * Format a server-provided byte count without exposing storage paths or other
 * implementation detail in the destructive-action dialog.
 */
export function formatGeneratedStorageBytes(bytes: number, locale = 'en-US'): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'

  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, unitIndex)
  const maximumFractionDigits = unitIndex === 0 || value >= 10 ? 0 : 1
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits }).format(value)} ${units[unitIndex]}`
}

/** Shared and unverifiable resources are always retained by the server. */
export function hasRetainedDeleteResources(count: number): boolean {
  return Number.isFinite(count) && count > 0
}
