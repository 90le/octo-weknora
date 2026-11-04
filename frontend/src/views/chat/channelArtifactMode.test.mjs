import assert from 'node:assert/strict'
import test from 'node:test'
import { isChannelArtifactMessage } from './channelArtifactMode.js'

test('artifact routing follows message origin while keeping restricted channel access', () => {
  assert.equal(isChannelArtifactMessage('web', 'octo', 'im'), true)
  assert.equal(isChannelArtifactMessage('web', 'octo', 'web'), false)
  assert.equal(isChannelArtifactMessage('web', undefined, 'web'), false)
  assert.equal(isChannelArtifactMessage('web', 'other', 'im'), false)
  assert.equal(isChannelArtifactMessage('channel', 'octo', 'im'), true)
  assert.equal(isChannelArtifactMessage('channel', 'octo', 'web'), true)
})
