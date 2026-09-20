import assert from 'node:assert/strict'
import test from 'node:test'
import type { OctoScope } from '@/api/octo'
import { resolveBusinessView, scopedIssueFilter, issueFilterKey, scopePath, issueSourceLabel, issueBelongsToContext } from './octoBusinessView'

const scope = (id: string, name: string, subarea = '', account = 'bot-a'): OctoScope => ({
  id, display_name: name, account_id: account, group_id: 'group-one', subarea_id: subarea,
  name_source: 'octo', sync_status: 'verified', sync_error: '', checked_at: null, verified_at: null,
  inherit_parent: false, allow_knowledge_creation: false, aggregate_child_issues: false,
})

test('controlled views override legacy nested-tab and deep-link selections', () => {
  assert.equal(resolveBusinessView('issues', 'contacts'), 'issues')
  assert.equal(resolveBusinessView('reports', 'schedule'), 'reports')
  assert.equal(resolveBusinessView('contacts', 'issues'), 'contacts')
  assert.equal(resolveBusinessView(undefined, 'schedule'), 'reports')
  assert.equal(resolveBusinessView(undefined, 'unknown'), 'issues')
})

test('reset or stale route filters cannot escape a knowledge-base context', () => {
  const filter = { knowledge_base_id: 'another-kb', kind: 'bug', status: 'open' }
  const requested = scopedIssueFilter(filter, 'current-kb', '', '')
  assert.equal(requested.knowledge_base_id, 'current-kb')
  assert.equal(filter.knowledge_base_id, 'another-kb', 'query building never changes pending UI controls')
  assert.deepEqual(scopedIssueFilter({}, 'current-kb', '', ''), { knowledge_base_id: 'current-kb' })
  assert.equal(scopedIssueFilter(filter, undefined, '', '').knowledge_base_id, 'another-kb')
})

test('date filters include the last local calendar day and reject invalid ranges', () => {
  const request = scopedIssueFilter({}, undefined, '2026-09-20', '2026-09-20')
  assert.equal(request.from, new Date(2026, 8, 20).toISOString())
  assert.equal(request.to, new Date(2026, 8, 21).toISOString())
  assert.throws(() => scopedIssueFilter({}, undefined, '2026-09-21', '2026-09-20'), /开始日期/)
  assert.throws(() => scopedIssueFilter({}, undefined, '2026-02-30', ''), /有效日期/)
})

test('changing controls invalidates a report without changing its captured request', () => {
  const controls = { scope_id: 'scope-a', status: 'open' }
  const captured = scopedIssueFilter(controls, 'kb-a', '2026-09-01', '2026-09-07')
  const originalKey = issueFilterKey(captured)
  controls.scope_id = 'scope-b'
  assert.equal(captured.scope_id, 'scope-a')
  assert.notEqual(issueFilterKey(scopedIssueFilter(controls, 'kb-a', '2026-09-01', '2026-09-07')), originalKey)
  assert.equal(issueFilterKey({ status: 'open', scope_id: 'scope-a' }), issueFilterKey({ scope_id: 'scope-a', status: 'open', keyword: '' }))
})

test('source labels show real parent and child names from the correct account', () => {
  const parent = scope('parent', 'AIBP 产品知识库')
  const child = scope('child', 'Bug 反馈', 'thread-one')
  const otherAccount = scope('other-parent', '另一个 Bot 的群名称', '', 'bot-b')
  const scopes = [otherAccount, parent, child]
  assert.equal(scopePath(child, scopes), 'AIBP 产品知识库 / Bug 反馈')
  assert.equal(issueSourceLabel({ is_direct: false, scope_id: 'child', scope_name: '旧子区名称', group_id: 'group-one', subarea_id: 'thread-one' }, scopes), 'AIBP 产品知识库 / Bug 反馈')
  assert.equal(issueSourceLabel({ is_direct: true, scope_id: '', scope_name: '', group_id: '', subarea_id: '' }, scopes), '私聊')
})

test('removed region metadata falls back to recorded names without inventing IDs as names', () => {
  assert.equal(issueSourceLabel({ is_direct: false, scope_id: 'removed', scope_name: '历史群名称', group_id: 'old-group', subarea_id: '' }, []), '历史群名称')
  assert.equal(issueSourceLabel({ is_direct: false, scope_id: 'removed-child', scope_name: '历史子区', group_id: 'old-group', subarea_id: 'thread' }, []), '所属群聊 / 历史子区')
})

test('details from an earlier knowledge-base context are not displayed in the new one', () => {
  assert.equal(issueBelongsToContext({ knowledge_base_id: 'kb-a' }, 'kb-b'), false)
  assert.equal(issueBelongsToContext({ knowledge_base_id: 'kb-b' }, 'kb-b'), true)
  assert.equal(issueBelongsToContext({ knowledge_base_id: 'kb-a' }), true)
})

