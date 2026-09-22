import assert from 'node:assert/strict'
import test from 'node:test'

import {
  canPreviewRestartRecoveryForSource,
  canStartRestartRecovery,
  isRestartRecoveryTerminal,
  summarizeRestartRecoveryCandidates,
} from './datasourceRestartRecoveryState'

test('restart recovery action is limited to administrators and GitHub document sources', () => {
  assert.equal(canPreviewRestartRecoveryForSource({ type: 'github', config: { settings: { mode: 'documents' } } }, true), true)
  assert.equal(canPreviewRestartRecoveryForSource({ type: 'github', config: { settings: { mode: 'source' } } }, true), false)
  assert.equal(canPreviewRestartRecoveryForSource({ type: 'notion', config: {} }, true), false)
  assert.equal(canPreviewRestartRecoveryForSource({ type: 'github', config: { settings: { mode: 'documents' } } }, false), false)
})

test('restart recovery only starts from a reviewed executable preview', () => {
  const preview = {
    preview_token: 'opaque', eligible_count: 1, excluded_count: 1, blocked_count: 0,
    blockers: [], candidates: [], data_source_id: 'ds', knowledge_base_id: 'kb', plan_digest: 'digest', expires_at: '',
  }
  assert.equal(canStartRestartRecovery(preview, false, false), true)
  assert.equal(canStartRestartRecovery({ ...preview, eligible_count: 0 }, false, false), false)
  assert.equal(canStartRestartRecovery({ ...preview, blockers: ['storage unreadable'] }, false, false), false)
  assert.equal(canStartRestartRecovery(preview, true, false), false)
  assert.equal(canStartRestartRecovery(preview, false, true), false)
})

test('restart recovery polling stops only on terminal run states', () => {
  assert.equal(isRestartRecoveryTerminal('pending'), false)
  assert.equal(isRestartRecoveryTerminal('running'), false)
  assert.equal(isRestartRecoveryTerminal('completed'), true)
  assert.equal(isRestartRecoveryTerminal('partial'), true)
  assert.equal(isRestartRecoveryTerminal('failed'), true)
  assert.equal(isRestartRecoveryTerminal('blocked'), true)
})

test('candidate summary preserves excluded URL-style rows for display', () => {
  const summary = summarizeRestartRecoveryCandidates([
    { knowledge_id: 'one', state: 'pending' },
    { knowledge_id: 'two', state: 'excluded', reason: 'URL and non-file candidates are excluded by default' },
    { knowledge_id: 'three', state: 'blocked', reason: 'canonical target already exists' },
  ])
  assert.deepEqual(summary, { eligible: 1, excluded: 1, blocked: 1 })
})
