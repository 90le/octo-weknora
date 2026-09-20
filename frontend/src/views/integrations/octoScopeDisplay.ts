import type { EffectiveBinding, OctoScope } from '@/api/octo'

export interface ScopeGroup {
  scope: OctoScope
  children: OctoScope[]
}

export interface ScopeKnowledgeRow {
  knowledgeBaseId: string
  name: string
  query: 'direct' | 'inherited' | 'none'
  sourceScopeId: string
  managed: boolean
}

export function groupScopes(scopes: OctoScope[], search = ''): ScopeGroup[] {
  const query = search.trim().toLocaleLowerCase()
  const matches = (scope: OctoScope) =>
    `${scope.display_name} ${scope.account_id} ${scope.group_id} ${scope.subarea_id}`.toLocaleLowerCase().includes(query)
  return scopes.filter(scope => !scope.subarea_id).map(scope => {
    const children = scopes.filter(child => child.subarea_id && child.account_id === scope.account_id && child.group_id === scope.group_id)
    return { scope, children: matches(scope) ? children : children.filter(matches) }
  }).filter(group => matches(group.scope) || group.children.length > 0)
}

// The explicit grant catalog is the authority for maintenance. A query binding
// (or a switch's descriptive label) must never be presented as a grant itself.
export function scopeKnowledgeRows(bindings: EffectiveBinding[], managedIds: string[], libraries: Array<{id:string;name:string}>): ScopeKnowledgeRow[] {
  const grants = new Set(managedIds)
  const rows = new Map<string, ScopeKnowledgeRow>()
  const create = (id: string): ScopeKnowledgeRow => ({
    knowledgeBaseId: id,
    name: libraries.find(kb => kb.id === id)?.name || '知识库名称暂不可用',
    query: 'none', sourceScopeId: '', managed: grants.has(id),
  })
  for (const binding of bindings) {
    const row = rows.get(binding.knowledge_base_id) || create(binding.knowledge_base_id)
    if (row.query !== 'direct') {
      row.query = binding.inherited ? 'inherited' : 'direct'
      row.sourceScopeId = binding.from_scope_id
    }
    rows.set(binding.knowledge_base_id, row)
  }
  for (const id of grants) if (!rows.has(id)) rows.set(id, create(id))
  return [...rows.values()]
}
