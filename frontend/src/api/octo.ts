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
}
export interface EffectiveBinding {
  knowledge_base_id: string
  from_scope_id: string
  inherited: boolean
  can_manage: boolean
}
type Envelope<T> = { success: boolean; data: T }
const root = '/api/v1/octo'
export const listScopes = (offset = 0) => get(`${root}/scopes?offset=${offset}`) as unknown as Promise<Envelope<OctoScope[]>>
export const createScope = (data: { account_id: string; group_id: string; subarea_id: string; inherit_parent: boolean }) => post(`${root}/scopes`, data) as unknown as Promise<Envelope<OctoScope>>
export const updateScope = (id: string, display_name: string, inherit_parent: boolean, allow_knowledge_creation = false, aggregate_child_issues = false) => put(`${root}/scopes/${encodeURIComponent(id)}`, { display_name, inherit_parent, allow_knowledge_creation, aggregate_child_issues })
export const effectiveBindings = (id: string) => get(`${root}/scopes/${encodeURIComponent(id)}/effective-bindings`) as unknown as Promise<Envelope<EffectiveBinding[]>>
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
