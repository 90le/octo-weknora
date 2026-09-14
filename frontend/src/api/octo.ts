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
}
export interface EffectiveBinding {
  knowledge_base_id: string
  from_scope_id: string
  inherited: boolean
}
type Envelope<T> = { success: boolean; data: T }
const root = '/api/v1/octo'
export const listScopes = (offset = 0) => get(`${root}/scopes?offset=${offset}`) as unknown as Promise<Envelope<OctoScope[]>>
export const createScope = (data: { account_id: string; group_id: string; subarea_id: string; inherit_parent: boolean }) => post(`${root}/scopes`, data) as unknown as Promise<Envelope<OctoScope>>
export const updateScope = (id: string, display_name: string, inherit_parent: boolean) => put(`${root}/scopes/${encodeURIComponent(id)}`, { display_name, inherit_parent })
export const effectiveBindings = (id: string) => get(`${root}/scopes/${encodeURIComponent(id)}/effective-bindings`) as unknown as Promise<Envelope<EffectiveBinding[]>>
export const bindKB = (scope: string, kb: string) => put(`${root}/scopes/${encodeURIComponent(scope)}/knowledge-bases/${encodeURIComponent(kb)}`, {})
export const unbindKB = (scope: string, kb: string) => del(`${root}/scopes/${encodeURIComponent(scope)}/knowledge-bases/${encodeURIComponent(kb)}`)
export const listConnections = () => get(`${root}/connections`) as unknown as Promise<Envelope<Array<{ account_id: string; updated_at: string }>>>
export const saveConnection = (account: string, token: string) => put(`${root}/connections/${encodeURIComponent(account)}/credentials`, { token })
export const syncScopeName = (id: string) => post(`${root}/scopes/${encodeURIComponent(id)}/sync`, {}) as unknown as Promise<Envelope<OctoScope>>
export const inspectMemberRole = (scope: string, uid: string) => get(`${root}/scopes/${encodeURIComponent(scope)}/members/${encodeURIComponent(uid)}/role`) as unknown as Promise<Envelope<{ uid: string; kind: string; role: string; can_manage_group: boolean; reason: string }>>
