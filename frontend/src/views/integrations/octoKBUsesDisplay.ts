import type { EffectiveScopeUse, OctoScope } from '@/api/octo'

export interface EffectiveUseGroup {
  key: string
  accountId: string
  groupId: string
  parentScope?: OctoScope
  uses: EffectiveScopeUse[]
}

function compareNames(a: string, b: string): number {
  return a.localeCompare(b, 'zh-CN')
}

// Group by both Bot connection and native group ID. Identical group IDs under
// different Bot connections must never be presented as one authorization area.
export function effectiveUseGroups(rows: EffectiveScopeUse[], scopes: OctoScope[]): EffectiveUseGroup[] {
  const groups = new Map<string, EffectiveUseGroup>()
  for (const row of rows) {
    const key = JSON.stringify([row.account_id, row.group_id])
    let group = groups.get(key)
    if (!group) {
      group = {
        key,
        accountId: row.account_id,
        groupId: row.group_id,
        parentScope: scopes.find(scope => scope.account_id === row.account_id && scope.group_id === row.group_id && !scope.subarea_id),
        uses: [],
      }
      groups.set(key, group)
    }
    group.uses.push(row)
  }
  return [...groups.values()]
    .map(group => ({ ...group, uses: group.uses.sort((a, b) => {
      if (!a.subarea_id && b.subarea_id) return -1
      if (a.subarea_id && !b.subarea_id) return 1
      return compareNames(a.display_name, b.display_name) || compareNames(a.scope_id, b.scope_id)
    }) }))
    .sort((a, b) => compareNames(a.parentScope?.display_name || a.uses[0]?.display_name || '', b.parentScope?.display_name || b.uses[0]?.display_name || '') || compareNames(a.key, b.key))
}

export function affectedInheritedUses(parent: EffectiveScopeUse, rows: EffectiveScopeUse[]): EffectiveScopeUse[] {
  if (parent.subarea_id || parent.query_mode !== 'direct') return []
  return rows.filter(row => row.account_id === parent.account_id && row.group_id === parent.group_id && row.query_mode === 'inherited' && row.from_scope_id === parent.scope_id)
    .sort((a, b) => compareNames(a.display_name, b.display_name) || compareNames(a.scope_id, b.scope_id))
}

export function directChildFallsBackToParent(row: EffectiveScopeUse, rows: EffectiveScopeUse[], scopes: OctoScope[]): boolean {
  if (!row.subarea_id || row.query_mode !== 'direct') return false
  const scope = scopes.find(scope => scope.id === row.scope_id)
  return Boolean(scope?.inherit_parent && rows.some(parent => parent.account_id === row.account_id && parent.group_id === row.group_id && !parent.subarea_id && parent.query_mode === 'direct'))
}

export function sortedDeletionImpact(rows: EffectiveScopeUse[]): EffectiveScopeUse[] {
  return [...rows].sort((a, b) =>
    compareNames(a.account_id, b.account_id) ||
    compareNames(a.group_id, b.group_id) ||
    (a.subarea_id ? 1 : 0) - (b.subarea_id ? 1 : 0) ||
    compareNames(a.display_name, b.display_name) ||
    compareNames(a.scope_id, b.scope_id))
}

// Include the current scope, any child inheriting it, and the parent direct
// binding of a direct child. A preview must be discarded if any of these
// conditions changes before confirmation, including the child's inheritance
// flag while it still has a direct binding.
export function scopeUnbindFingerprint(scope: OctoScope, uses: EffectiveScopeUse[]): string {
  return JSON.stringify(uses.filter(use =>
    use.scope_id === scope.id ||
    (use.query_mode === 'inherited' && use.from_scope_id === scope.id) ||
    (Boolean(scope.subarea_id) && !use.subarea_id && use.account_id === scope.account_id && use.group_id === scope.group_id && use.query_mode === 'direct'))
    .map(use => [use.scope_id, use.display_name, use.query_mode, use.from_scope_id, use.can_manage, use.inherit_parent])
    .sort((a, b) => String(a[0]).localeCompare(String(b[0]))))
}
