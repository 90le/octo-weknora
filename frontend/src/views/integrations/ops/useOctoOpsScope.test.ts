import test from 'node:test'
import assert from 'node:assert/strict'
import { createDefaultScope, partitionTargetsForBot, resolveScopeQuery } from './useOctoOpsScope.ts'

test('resolveScopeQuery handles default 14d, private DM type, specific scope and custom range', () => {
  const s = createDefaultScope()
  assert.equal(s.days, 14)

  const dmScope = { ...s, sourceValue: 'type:dm', kbId: 'kb-media', keyword: ' 聚光 ' }
  const q1 = resolveScopeQuery(dmScope)
  assert.equal(q1.scope_id, '')
  assert.equal(q1.source_type, 'dm')
  assert.equal(q1.knowledge_base_id, 'kb-media')
  assert.equal(q1.keyword, '聚光')

  const specific = { ...s, sourceValue: 'scope:sc-1', days: 0, customFrom: '2026-11-01', customTo: '2026-11-15' }
  const q2 = resolveScopeQuery(specific)
  assert.equal(q2.scope_id, 'sc-1')
  assert.equal(q2.source_type, '')
  assert.ok(q2.from < q2.to)
})

test('partitionTargetsForBot separates valid shared targets from incompatible targets on bot switch', () => {
  const targets = [
    { key: 'group:g-mgmt', kind: 'group' as const, group_id: 'g-mgmt', label: '管理层周报群', channel_kind: '主群' },
    { key: 'group:g-only-a', kind: 'group' as const, group_id: 'g-only-a', label: '内测群A', channel_kind: '主群' },
    { key: 'dm:uid-qbb', kind: 'dm' as const, uid: 'uid-qbb', label: '邱彬彬', channel_kind: '私聊' },
    { key: 'dm:uid-linm', kind: 'dm' as const, uid: 'uid-linm', label: '林敏', channel_kind: '私聊' },
  ]
  const res = partitionTargetsForBot(targets, new Set(['group:g-mgmt']), new Set(['uid-qbb']))
  assert.deepEqual(res.valid.map((t) => t.key), ['group:g-mgmt', 'dm:uid-qbb'])
  assert.deepEqual(res.invalid.map((t) => t.key), ['group:g-only-a', 'dm:uid-linm'])
})
