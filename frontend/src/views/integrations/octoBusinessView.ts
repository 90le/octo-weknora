import type { OctoScope } from '@/api/octo'
import type { IssueFilter, OctoIssue } from '@/api/octo-business'

export type OctoBusinessView = 'issues' | 'reports' | 'contacts'

export function resolveBusinessView(controlled?: OctoBusinessView, fallback?: unknown): OctoBusinessView {
  if (controlled) return controlled
  if (fallback === 'reports' || fallback === 'schedule') return 'reports'
  if (fallback === 'contacts') return 'contacts'
  return 'issues'
}

function localDay(value: string): Date {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) throw new Error('请输入有效日期')
  const [year, month, day] = value.split('-').map(Number)
  const date = new Date(year!, month! - 1, day!)
  if (date.getFullYear() !== year || date.getMonth() + 1 !== month || date.getDate() !== day) {
    throw new Error('请输入有效日期')
  }
  return date
}

/** A contextual KB is authoritative even after resetting filters or reading an old deep link. */
export function scopedIssueFilter(filter: IssueFilter, knowledgeBaseId: string | undefined, fromDay: string, toDay: string): IssueFilter {
  const result: IssueFilter = { ...filter }
  if (knowledgeBaseId) result.knowledge_base_id = knowledgeBaseId
  delete result.from
  delete result.to
  if (fromDay) result.from = localDay(fromDay).toISOString()
  if (toDay) {
    const end = localDay(toDay)
    end.setDate(end.getDate() + 1)
    result.to = end.toISOString()
  }
  if (result.from && result.to && result.from >= result.to) throw new Error('开始日期不能晚于结束日期')
  return result
}

/** Compare the requested data scope, not the order in which controls were edited. */
export function issueFilterKey(filter: IssueFilter): string {
  return JSON.stringify(Object.entries(filter).filter(([, value]) => value !== undefined && value !== '').sort(([left], [right]) => left.localeCompare(right)))
}

export function scopePath(scope: OctoScope, scopes: OctoScope[]): string {
  if (!scope.subarea_id) return scope.display_name || '未命名群聊'
  const parent = scopes.find(item => item.account_id === scope.account_id && item.group_id === scope.group_id && !item.subarea_id)
  return `${parent?.display_name || '所属群聊'} / ${scope.display_name || '未命名子区'}`
}

export function issueSourceLabel(issue: Pick<OctoIssue, 'is_direct' | 'scope_id' | 'scope_name' | 'group_id' | 'subarea_id'>, scopes: OctoScope[]): string {
  if (issue.is_direct) return '私聊'
  const current = scopes.find(scope => scope.id === issue.scope_id)
  if (current) return scopePath(current, scopes)
  const parent = scopes.find(scope => scope.group_id === issue.group_id && !scope.subarea_id)
  if (issue.subarea_id) return `${parent?.display_name || '所属群聊'} / ${issue.scope_name || '原子区'}`
  return parent?.display_name || issue.scope_name || '原群聊'
}

export function issueBelongsToContext(issue: Pick<OctoIssue, 'knowledge_base_id'>, knowledgeBaseId?: string): boolean {
  return !knowledgeBaseId || issue.knowledge_base_id === knowledgeBaseId
}
