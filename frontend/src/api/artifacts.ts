import { del, get, getDown } from '@/utils/request'

/**
 * One file in the artifact library: the latest version of a file a skill
 * generated in one of the caller's sessions. Download it through
 * downloadArtifact(session_id, message_id, index) from '@/api/chat', which
 * re-checks session ownership server side.
 */
export interface ArtifactLibraryItem {
  source?: 'web' | 'channel'
  channel_id?: string
  channel_platform?: string
  im_channel_artifact?: boolean
  session_id: string
  session_title: string
  message_id: string
  index: number
  /** `resource://<handle>` when the deployment runs a resource catalog. */
  handle?: string
  file_name: string
  file_type: string
  file_size: number
  source_path: string
  created_at: string
  /** How many times this file was (re)generated in its session. */
  version_count: number
}

export interface ArtifactLibraryParams {
  keyword?: string
  source?: 'web' | 'channel'
  channelId?: string
  /** Extensions such as ".pdf"; empty means every type. */
  fileTypes?: string[]
  page?: number
  pageSize?: number
}

export interface ArtifactLibraryPage {
  success: boolean
  data: ArtifactLibraryItem[]
  total: number
  page: number
  page_size: number
  source?: string
  channel_id?: string
  source_options?: ArtifactSourceOption[]
}

export function listArtifactLibrary(params: ArtifactLibraryParams = {}) {
  const query: Record<string, string | number> = {}
  const keyword = params.keyword?.trim()
  if (keyword) query.keyword = keyword
  if (params.source) query.source = params.source
  if (params.channelId) query.channel_id = params.channelId
  if (params.fileTypes?.length) query.file_types = params.fileTypes.join(',')
  if (params.page) query.page = params.page
  if (params.pageSize) query.page_size = params.pageSize
  return get<ArtifactLibraryPage>('/api/v1/artifacts', { params: query })
}

export interface ArtifactDeleteResult {
  success: boolean
  data: { file_name: string; deleted: number }
}

/**
 * Deletes a file from the artifact library, along with every earlier version of
 * it in the same session — a library row is the file, not one regeneration of
 * it, so leaving the older versions behind would leave the row in place.
 *
 * The stored bytes are reclaimed unless something else still references them,
 * so this is not reversible. Callers confirm with the user first.
 */
export function deleteArtifactLibraryItem(item: ArtifactLibraryItem) {
  const query = new URLSearchParams({
    session_id: item.session_id,
    message_id: item.message_id,
    index: String(item.index),
  })
  return del<ArtifactDeleteResult>(`/api/v1/artifacts?${query.toString()}`)
}

export function downloadChannelArtifact(item: Pick<ArtifactLibraryItem, 'session_id' | 'message_id' | 'index'>): Promise<Blob> {
  return getDown(`/api/v1/artifacts/channels/${encodeURIComponent(item.session_id)}/messages/${encodeURIComponent(item.message_id)}/${item.index}/download`)
}

// Full, unfiltered aggregate supplied by the artifact usage endpoint.
export interface ArtifactUsageResponse {
  success: boolean
  data: { file_count: number; logical_bytes: number; scope: 'accessible_artifact_versions' }
}

export function getArtifactUsage() {
  return get<ArtifactUsageResponse>('/api/v1/artifacts/usage')
}

// Channel deletion requires a distinct server-side authorization check.
export function deleteChannelArtifact(item: ArtifactLibraryItem) {
  return del<ArtifactDeleteResult>(`/api/v1/artifacts/channels/${encodeURIComponent(item.session_id)}/messages/${encodeURIComponent(item.message_id)}/${item.index}?all_versions=true`)
}

// Supplied by the list endpoint, scoped to the current user and tenant.
export interface ArtifactSourceOption { source: 'web' | 'channel'; channel_id?: string; platform?: string; name?: string }

// The server authorizes the caller's bound channel account on every page.
export function getChannelArtifactChat(sessionId: string, limit = 20, beforeTime = '', beforeId = '') {
  return get<{ success: boolean; data: { session: any; messages: any[] } }>(
    `/api/v1/artifacts/channels/${encodeURIComponent(sessionId)}/chat`,
    { params: { limit, ...(beforeTime ? { before_time: beforeTime, before_id: beforeId } : {}) } },
  )
}
