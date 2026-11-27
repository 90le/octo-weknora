import { reactive } from 'vue'
import type { DeliveryTarget } from '@/api/octo-business'

export interface OctoOpsScopeState {
  keyword: string
  sourceValue: string // '' | 'type:group' | 'type:subarea' | 'type:dm' | 'scope:<id>'
  kbId: string
  channelId: string
  days: number // 7 | 14 | 30 | 0 (custom)
  customFrom: string
  customTo: string
}

function isoDateOffset(daysAgo: number): string {
  const d = new Date()
  d.setDate(d.getDate() - daysAgo)
  return d.toISOString().slice(0, 10)
}

export function createDefaultScope(): OctoOpsScopeState {
  return {
    keyword: '',
    sourceValue: '',
    kbId: '',
    channelId: '',
    days: 14,
    customFrom: isoDateOffset(14),
    customTo: isoDateOffset(0),
  }
}

export const opsScope = reactive<OctoOpsScopeState>(createDefaultScope())

export function resetOpsScope() {
  opsScope.keyword = ''
  opsScope.sourceValue = ''
  opsScope.kbId = ''
  opsScope.channelId = ''
}

export function resolveScopeQuery(state: OctoOpsScopeState): {
  scope_id: string
  source_type: string
  knowledge_base_id: string
  channel_id: string
  keyword: string
  from: string
  to: string
} {
  let scope_id = ''
  let source_type = ''
  if (state.sourceValue.startsWith('scope:')) {
    scope_id = state.sourceValue.slice(6)
  } else if (state.sourceValue.startsWith('type:')) {
    source_type = state.sourceValue.slice(5)
  }
  const now = new Date()
  let from: Date
  let to: Date = now
  if (state.days === 0 && state.customFrom && state.customTo) {
    from = new Date(`${state.customFrom}T00:00:00`)
    to = new Date(`${state.customTo}T23:59:59`)
  } else {
    const span = state.days || 14
    from = new Date(now.getTime() - span * 86400_000)
  }
  return {
    scope_id,
    source_type,
    knowledge_base_id: state.kbId,
    channel_id: state.channelId,
    keyword: state.keyword.trim(),
    from: from.toISOString(),
    to: to.toISOString(),
  }
}

/**
 * Remaps selected multi-target delivery destinations when the operator switches
 * the sending Octo Bot, flagging destinations not belonging to the new Bot.
 */
export function partitionTargetsForBot(
  targets: DeliveryTarget[],
  validGroupKeys: Set<string>,
  verifiedFriendUIDs: Set<string>,
): { valid: DeliveryTarget[]; invalid: DeliveryTarget[] } {
  const valid: DeliveryTarget[] = []
  const invalid: DeliveryTarget[] = []
  for (const t of targets) {
    if (t.kind === 'group') {
      if (validGroupKeys.has(t.key)) valid.push(t)
      else invalid.push(t)
    } else if (t.kind === 'dm') {
      if (t.uid && verifiedFriendUIDs.has(t.uid)) valid.push(t)
      else invalid.push(t)
    }
  }
  return { valid, invalid }
}
