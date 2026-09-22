import { get, post, put, del } from '../../utils/request'

// --- Types ---

export interface DataSource {
  id: string
  tenant_id: number
  knowledge_base_id: string
  name: string
  type: string
  config: any
  sync_schedule: string
  sync_mode: 'incremental' | 'full'
  status: 'active' | 'paused' | 'error'
  conflict_strategy: 'overwrite' | 'skip'
  sync_deletions: boolean
  last_sync_at: string | null
  last_sync_result: any
  error_message: string
  // Single-field "credentials" map from the main response — DataSource
  // credentials are a per-connector atomic set.
  credentials?: { credentials: { configured: boolean } }
  created_at: string
  updated_at: string
  latest_sync_log?: SyncLog
}

/**
 * One user-facing failure sample. The backend sends a stable i18n `code`
 * (+ interpolation `params`) so the UI localises it to the viewer's language;
 * `message` is a fallback when no i18n key exists (old logs decode into it).
 */
export interface SyncItemError {
  title?: string
  code?: string
  params?: Record<string, string>
  message?: string
}

export interface SyncResultDetail {
  total?: number
  created?: number
  updated?: number
  deleted?: number
  skipped?: number
  failed?: number
  /** Per-item failure samples (capped); localised in the sync-log drawer. */
  errors?: SyncItemError[]
}

export interface SyncLog {
  id: string
  data_source_id: string
  status: 'running' | 'success' | 'partial' | 'failed' | 'canceled'
  started_at: string
  finished_at: string | null
  items_total: number
  items_created: number
  items_updated: number
  items_deleted: number
  items_skipped: number
  items_failed: number
  error_message: string
  result?: SyncResultDetail
}

export interface ConnectorMeta {
  type: string
  name: string
  description: string
  icon: string
  priority: number
  auth_type: string
  capabilities: string[]
}

export interface Resource {
  external_id: string
  name: string
  type: string
  description: string
  url: string
  parent_id?: string
  has_children?: boolean
}

/** One repository returned by the native GitHub organization/user discovery API. */
export interface GitHubRepository {
  /** Canonical GitHub owner/repository name. */
  repository: string
  default_branch: string
  archived: boolean
  disabled?: boolean
  fork?: boolean
  size_kib?: number
  description: string
}

export interface GitHubDiscoveryResponse {
  owner: string
  repositories: GitHubRepository[]
  next_cursor?: string
}

export type GitHubBulkMode = 'source' | 'documents'
export type GitHubBatchSyncPolicy = 'manual' | 'scheduled'

export interface GitHubBatchResultItem {
  repository: string
  /** A source was created, already existed, failed validation, or was skipped. */
  status: 'created' | 'existing' | 'failed' | 'skipped'
  data_source_id?: string
  message?: string
}

export interface GitHubBatchResponse {
  owner: string
  results: GitHubBatchResultItem[]
}

/**
 * `detach` only stops the source and preserves documents already indexed by
 * it. `purge_generated` additionally removes content which the server can
 * prove belongs exclusively to this data source.
 */
export type DataSourceDeleteMode = 'detach' | 'purge_generated'

/**
 * Server-side deletion preview. The token is intentionally opaque: the UI
 * must send it back unchanged so the server can reject stale destructive
 * operations instead of acting on a preview that no longer matches.
 */
export interface DataSourceDeletePreview {
  datasource_id: string
  generated_knowledge_count: number
  generated_storage_bytes: number
  shared_or_unverifiable_resources_count: number
  legacy_unverifiable_resources_count: number
  preview_token: string
  expires_at: string
}

export interface DeleteDataSourceWithModeRequest {
  mode: DataSourceDeleteMode
  preview_token: string
}

// --- API calls ---

export function getConnectorTypes() {
  return get('/api/v1/datasource/types')
}

export function listDataSources(kbId: string) {
  return get(`/api/v1/datasource?kb_id=${encodeURIComponent(kbId)}`)
}

export function getDataSource(id: string) {
  return get(`/api/v1/datasource/${id}`)
}

export function createDataSource(data: Partial<DataSource>) {
  return post('/api/v1/datasource', data)
}

export function updateDataSource(id: string, data: Partial<DataSource>) {
  return put(`/api/v1/datasource/${id}`, data)
}

export function deleteDataSource(id: string) {
  return del(`/api/v1/datasource/${id}`)
}

/**
 * Reads a bounded impact preview before the user removes a source. This is a
 * separate endpoint from the legacy DELETE request above so editor cleanup
 * paths can retain their established detach-only behaviour.
 */
export function previewDataSourceDeletion(id: string) {
  return post<DataSourceDeletePreview>(`/api/v1/datasource/${id}/delete-preview`, {})
}

/**
 * Removes a source using the preview token issued by previewDataSourceDeletion.
 * The server owns authorization, shared-resource checks, and token expiry.
 */
export function deleteDataSourceWithMode(id: string, data: DeleteDataSourceWithModeRequest) {
  return post(`/api/v1/datasource/${id}/delete`, data)
}

export function validateConnection(id: string) {
  return post(`/api/v1/datasource/${id}/validate`, {})
}

// Validate credentials without persisting (for "Test Connection" during creation)
export function validateCredentials(type: string, credentials: Record<string, any>) {
  return post('/api/v1/datasource/validate-credentials', { type, credentials })
}

/**
 * Lists repositories belonging to one GitHub organization or user without
 * persisting a data source. Credentials are sent only in the request body and
 * never stored in browser state beyond the active bulk-import drawer.
 */
export function discoverGitHubRepositories(
  owner: string,
  credentials?: Record<string, unknown>,
  cursor?: string,
) {
  return post('/api/v1/datasource/github/discover', { owner, credentials, cursor })
}

/**
 * Creates one normal GitHub data source per selected repository. Keeping rows
 * separate preserves existing per-repository sync logs, source snapshots and
 * failure handling in the standard data-source UI.
 */
export function createGitHubDataSourceBatch(data: {
  knowledge_base_id: string
  owner: string
  repositories: GitHubRepository[]
  credentials?: Record<string, unknown>
  mode: GitHubBulkMode
  paths?: string[]
  sync_policy?: GitHubBatchSyncPolicy
  sync_schedule?: string
  start_sync?: boolean
}) {
  return post('/api/v1/datasource/github/batch', data)
}

// listResources lists selectable resources for a data source. Pass parentId to
// lazily load the direct children of a resource (e.g. expanding a Feishu wiki
// space/node), which avoids traversing the whole tree up front.
export function listResources(id: string, parentId?: string) {
  const query = parentId ? `?parent_id=${encodeURIComponent(parentId)}` : ''
  return get(`/api/v1/datasource/${id}/resources${query}`, { timeout: 120000 })
}

// resolveResourceAncestors returns the ExternalIDs of every parent that must be
// expanded to reveal the given (possibly deeply nested) selections in a lazily
// loaded picker. Used when editing a data source to restore an existing selection.
export function resolveResourceAncestors(id: string, resourceIds: string[]) {
  return post(`/api/v1/datasource/${id}/resource-ancestors`, { resource_ids: resourceIds }, { timeout: 120000 })
}

export function triggerSync(id: string) {
  return post(`/api/v1/datasource/${id}/sync`, {})
}

export function pauseDataSource(id: string) {
  return post(`/api/v1/datasource/${id}/pause`, {})
}

export function resumeDataSource(id: string) {
  return post(`/api/v1/datasource/${id}/resume`, {})
}

export function getSyncLogs(id: string, limit = 20, offset = 0) {
  return get(`/api/v1/datasource/${id}/logs?limit=${limit}&offset=${offset}`)
}

// ----------------------------------------------------------------------------
// Data source credential subresource. Unlike the other three resources,
// DataSource exposes a single logical field "credentials" because connector
// auth is a per-connector atomic map. See internal/handler/dto/datasource.go.
// ----------------------------------------------------------------------------

export interface DataSourceCredentialsResponse {
  fields: {
    credentials: { configured: boolean }
  }
}

export async function putDataSourceCredentials(
  id: string,
  credentials: Record<string, unknown>,
): Promise<DataSourceCredentialsResponse> {
  const response: any = await put(`/api/v1/datasource/${id}/credentials`, { credentials })
  return (response.data ?? response) as DataSourceCredentialsResponse
}

export async function deleteDataSourceCredentials(id: string): Promise<void> {
  await del(`/api/v1/datasource/${id}/credentials/credentials`)
}
