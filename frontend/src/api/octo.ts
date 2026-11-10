import { get, post, put, del } from '@/utils/request'

export interface OctoScope {
  id: string
  account_id: string
  group_id: string
  subarea_id: string
  display_name: string
  name_source: string
  sync_status: string
  sync_error: string
  checked_at: string | null
  verified_at: string | null
  inherit_parent: boolean
  allow_knowledge_creation: boolean
  aggregate_child_issues: boolean
  allow_public_web: boolean
}
export interface EffectiveBinding {
  knowledge_base_id: string
  from_scope_id: string
  inherited: boolean
  can_manage: boolean
}
export interface EffectiveScopeUse {
  scope_id: string
  display_name: string
  account_id: string
  group_id: string
  subarea_id: string
  inherit_parent: boolean
  query_mode: 'direct' | 'inherited' | 'none'
  from_scope_id: string
  can_manage: boolean
  name_source: string
  sync_status: string
  verified_at: string | null
}
type Envelope<T> = { success: boolean; data: T }
const root = '/api/v1/octo'
export const listScopes = (offset = 0) => get(`${root}/scopes?offset=${offset}`) as unknown as Promise<Envelope<OctoScope[]>>
export const createScope = (data: { account_id: string; group_id: string; subarea_id: string; inherit_parent: boolean }) => post(`${root}/scopes`, data) as unknown as Promise<Envelope<OctoScope>>
export const updateScope = (id: string, display_name: string, inherit_parent: boolean, allow_knowledge_creation = false, aggregate_child_issues = false, allow_public_web = false) => put(`${root}/scopes/${encodeURIComponent(id)}`, { display_name, inherit_parent, allow_knowledge_creation, aggregate_child_issues, allow_public_web })
export const effectiveBindings = (id: string) => get(`${root}/scopes/${encodeURIComponent(id)}/effective-bindings`) as unknown as Promise<Envelope<EffectiveBinding[]>>
export const effectiveKBUses = (id: string) => get(`${root}/knowledge-bases/${encodeURIComponent(id)}/effective-scopes`) as unknown as Promise<Envelope<EffectiveScopeUse[]>>
export const managedKBs = (id: string) => get(`${root}/scopes/${encodeURIComponent(id)}/managed-knowledge-bases`) as unknown as Promise<Envelope<string[]>>
export const revokeKBManagement = (scope: string, kb: string) => del(`${root}/scopes/${encodeURIComponent(scope)}/managed-knowledge-bases/${encodeURIComponent(kb)}`)
export const bindKB = (scope: string, kb: string, can_manage?: boolean) => put(`${root}/scopes/${encodeURIComponent(scope)}/knowledge-bases/${encodeURIComponent(kb)}`, can_manage === undefined ? {} : { can_manage })
export const unbindKB = (scope: string, kb: string) => del(`${root}/scopes/${encodeURIComponent(scope)}/knowledge-bases/${encodeURIComponent(kb)}`)
export const listConnections = () => get(`${root}/connections`) as unknown as Promise<Envelope<Array<{ account_id: string; updated_at: string }>>>
export interface OctoIdentity { bot_uid: string; name: string; owner_uid?: string }
export interface AvailableOctoScope { group_id: string; subarea_id: string; name: string }
export const availableScopes = (account: string, group = '', page = 1) => get(`${root}/connections/${encodeURIComponent(account)}/available-scopes?group_id=${encodeURIComponent(group)}&page=${page}`) as unknown as Promise<Envelope<AvailableOctoScope[]>>
export const probeConnection = (token: string) => post(`${root}/connections/probe`, { token }) as unknown as Promise<Envelope<OctoIdentity>>
export const connectionIdentity = (account: string) => get(`${root}/connections/${encodeURIComponent(account)}/identity`) as unknown as Promise<Envelope<OctoIdentity>>
export const saveConnection = (account: string, token: string, expected_bot_uid?: string) => put(`${root}/connections/${encodeURIComponent(account)}/credentials`, { token, expected_bot_uid }) as unknown as Promise<Envelope<OctoIdentity>>
export const syncScopeName = (id: string) => post(`${root}/scopes/${encodeURIComponent(id)}/sync`, {}) as unknown as Promise<Envelope<OctoScope>>
export const inspectMemberRole = (scope: string, uid: string) => get(`${root}/scopes/${encodeURIComponent(scope)}/members/${encodeURIComponent(uid)}/role`) as unknown as Promise<Envelope<{ uid: string; kind: string; role: string; can_manage_group: boolean; reason: string }>>
export interface OctoChannelEvent {message_id:string;state:string;attempts:number;error_code:string;created_at:string;updated_at:string}
export const channelEvents=(id:string)=>get(`/api/v1/im-channels/${encodeURIComponent(id)}/events`) as unknown as Promise<Envelope<OctoChannelEvent[]>>

export interface OctoPage<T> { items: T[]; total: number }
export interface OctoGroupEntry { group_id: string; display_name: string; bot_count: number; subarea_count: number; pending: number }
export interface OctoBotEntry { account_id: string; name: string; connected: boolean; verified: boolean; group_count: number }
export interface OctoAreaEntry { subarea_id: string; display_name: string }
export interface OctoComparison { bot: OctoBotEntry; scope: OctoScope | null; query_count: number; manage_count: number }
export interface OctoScopeConfiguration {
  query_ids: string[]
  manage_ids: string[]
  inherit_parent: boolean
  allow_knowledge_creation: boolean
  aggregate_child_issues: boolean
  allow_public_web: boolean
}
export interface OctoConfigurationSnapshot {
  scope: OctoScope
  query_ids: string[]
  manage_ids: string[]
  parent_query_ids: string[]
  child_areas: OctoAreaEntry[]
  revision: string
}
export interface OctoAccessState { query_mode: 'none' | 'inherited' | 'direct'; can_manage: boolean }
export interface OctoConfigurationPreview {
  token: string
  impacts: Array<{ scope_id: string; display_name: string; subarea_id: string; changes: Array<{
    knowledge_base_id: string; before: OctoAccessState; after: OctoAccessState
  }> }>
}
type DirectoryQuery = { q?: string; account_id?: string; group_id?: string; subarea_id?: string; filter?: string; limit?: number; offset?: number }
function directory<T>(path: string, query: DirectoryQuery) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) if (value !== undefined) params.set(key, String(value))
  return get(`${root}/directory/${path}?${params}`) as unknown as Promise<Envelope<OctoPage<T>>>
}
export const directoryGroups = (q: DirectoryQuery) => directory<OctoGroupEntry>('groups', q)
export const directoryBots = (q: DirectoryQuery) => directory<OctoBotEntry>('bots', q)
export const directoryAreas = (q: DirectoryQuery) => directory<OctoAreaEntry>('areas', q)
export const directoryComparison = (q: DirectoryQuery) => directory<OctoComparison>('comparison', q)
const configPath = (id: string) => `${root}/scopes/${encodeURIComponent(id)}/configuration`
export const readOctoConfiguration = (id: string) => get(configPath(id)) as unknown as Promise<Envelope<OctoConfigurationSnapshot>>
export const previewOctoConfiguration = (id: string, revision: string, configuration: OctoScopeConfiguration) =>
  post(`${configPath(id)}/preview`, { revision, configuration }) as unknown as Promise<Envelope<OctoConfigurationPreview>>
export const saveOctoConfiguration = (id: string, revision: string, token: string, configuration: OctoScopeConfiguration) =>
  put(configPath(id), { revision, token, configuration }) as unknown as Promise<Envelope<OctoConfigurationPreview>>
export const removeOctoScope = (id: string, revision: string) =>
  del(`${root}/scopes/${encodeURIComponent(id)}?revision=${encodeURIComponent(revision)}`)
export const previewOctoAttachment = (target: { account_id: string; group_id: string; subarea_id: string }) =>
  post(`${root}/scopes/attach-preview`, target) as unknown as Promise<Envelope<OctoAreaEntry>>
