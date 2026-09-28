import test from 'node:test'
import assert from 'node:assert/strict'
import type { EffectiveScopeUse, OctoScope } from '@/api/octo'
import { affectedInheritedUses, directChildFallsBackToParent, effectiveUseGroups, scopeUnbindFingerprint, sortedDeletionImpact } from './octoKBUsesDisplay'

const scope = (id: string, account_id: string, group_id: string, subarea_id = '', inherit_parent = false): OctoScope => ({
  id, account_id, group_id, subarea_id, display_name: id, name_source: 'octo', sync_status: 'verified', sync_error: '', checked_at: null, verified_at: null,
  inherit_parent, allow_knowledge_creation: false, aggregate_child_issues: false, allow_public_web: false,
})
const use = (scope_id: string, account_id: string, group_id: string, subarea_id: string, query_mode: EffectiveScopeUse['query_mode'], from_scope_id = '', can_manage = false, inherit_parent = false): EffectiveScopeUse => ({
  scope_id, display_name: scope_id, account_id, group_id, subarea_id, inherit_parent, query_mode, from_scope_id, can_manage,
  name_source: 'octo', sync_status: 'verified', verified_at: null,
})

test('reverse use groups separate Bot connections and retain inherited and management-only rows', () => {
  const rows = [use('child','bot-a','same','sub','inherited','parent'), use('only-maintain','bot-a','same','manage','none','',true), use('other','bot-b','same','','direct','other')]
  const groups = effectiveUseGroups(rows, [scope('parent','bot-a','same'), scope('other','bot-b','same')])
  assert.equal(groups.length, 2)
  const byAccount = new Map(groups.map(group => [group.accountId, group]))
  assert.deepEqual(byAccount.get('bot-a')?.uses.map(row => [row.scope_id,row.query_mode,row.can_manage]), [['child','inherited',false], ['only-maintain','none',true]])
  assert.deepEqual(byAccount.get('bot-b')?.uses.map(row => row.scope_id), ['other'])
})

test('removing a parent direct binding only affects children inheriting that exact scope', () => {
  const parent = use('parent','bot-a','same','','direct','parent')
  const rows = [parent, use('child','bot-a','same','sub','inherited','parent'), use('direct-child','bot-a','same','direct','direct','direct-child'), use('other','bot-b','same','sub','inherited','other-parent')]
  assert.deepEqual(affectedInheritedUses(parent, rows).map(row => row.scope_id), ['child'])
  assert.deepEqual(affectedInheritedUses(rows[1]!, rows), [])
})

test('removing a child direct binding can leave inherited parent query, without changing maintenance grant', () => {
  const rows = [use('parent','bot-a','group','','direct','parent'), use('child','bot-a','group','sub','direct','child',true)]
  assert.equal(directChildFallsBackToParent(rows[1]!, rows, [scope('parent','bot-a','group'),scope('child','bot-a','group','sub',true)]), true)
  assert.equal(rows[1]?.can_manage, true)
  assert.equal(directChildFallsBackToParent(rows[1]!, rows, [scope('parent','bot-a','group'),scope('child','bot-a','group','sub',false)]), false)
})

test('whole-library deletion impact includes read inheritance and maintenance-only scopes', () => {
  const rows = [use('child','bot-a','group','sub','inherited','parent'), use('grant-only','bot-a','group','grant','none','',true), use('parent','bot-a','group','','direct','parent')]
  assert.deepEqual(sortedDeletionImpact(rows).map(row => [row.scope_id,row.query_mode]), [['parent','direct'],['child','inherited'],['grant-only','none']])
  assert.deepEqual(sortedDeletionImpact([]), [])
})

test('unbind confirmation becomes stale when a child loses parent read or inheritance', () => {
  const child = scope('child', 'bot-a', 'group', 'sub', true)
  const childUse = use('child', 'bot-a', 'group', 'sub', 'direct', 'child', false, true)
  const parentUse = use('parent', 'bot-a', 'group', '', 'direct', 'parent')
  const before = scopeUnbindFingerprint(child, [childUse, parentUse])
  assert.notEqual(before, scopeUnbindFingerprint(child, [childUse]), 'parent unbind changes fallback')
  assert.notEqual(before, scopeUnbindFingerprint(child, [{ ...childUse, inherit_parent: false }, parentUse]), 'inheritance toggle changes fallback')
  assert.equal(before, scopeUnbindFingerprint(child, [childUse, parentUse, use('foreign', 'bot-b', 'group', '', 'direct', 'foreign')]), 'foreign Bot changes cannot alter this preview')
})
