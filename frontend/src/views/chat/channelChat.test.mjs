import { readFileSync } from 'node:fs'
import assert from 'node:assert/strict'
import test from 'node:test'
import vm from 'node:vm'

// Exercise the production loader with deferred requests so route changes can
// occur between the ordinary session lookup and the restricted channel read.
const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const loader = source.slice(source.indexOf('const loadSessionAndHydrate = async (sid) => {'), source.indexOf('\nconst inputFieldRef'))
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
function setup(getSession, getChannelArtifactChat) {
  const state = {
    props: { embeddedMode: false }, firstQuery: { value: '' }, session_id: { value: 'channel' },
    sessionLoadVersion: 0, chatMode: { value: 'loading' }, currentSession: { value: null },
    hasMoreHistory: { value: true }, historyLoading: { value: true }, limit: { value: 2 },
    created_at: { value: '' }, channelBeforeId: { value: '' }, useSettingsStoreInstance: { hydrateSessionInputState() {} },
    getSession, getChannelArtifactChat, console: { error() {} }, batches: [],
  }
  state.handleMsgList = async batch => state.batches.push(batch)
  vm.createContext(state)
  state.load = vm.runInContext(`${loader}; loadSessionAndHydrate`, state)
  return state
}
test('fallback loads native messages and keeps artifacts for pagination', async () => {
  const artifact = { index: 0, file_name: 'report.pdf' }
  const state = setup(async () => { throw { status: 404 } }, async () => ({ success: true, data: { session: { id: 'channel' }, messages: [
    { id: '1', created_at: '2026-01-01', role: 'user', content: 'hello' },
    { id: '2', role: 'assistant', content: '**answer**', artifacts: [artifact], knowledge_references: [{ id: 'ref' }] },
  ] } }))
  await state.load('channel')
  assert.equal(state.chatMode.value, 'channel')
  assert.equal(state.hasMoreHistory.value, true)
  assert.equal(state.created_at.value, '2026-01-01')
  assert.equal(state.channelBeforeId.value, '1')
  assert.equal(state.batches[0][1].artifacts[0], artifact)
  assert.equal(state.batches[0][1].knowledge_references[0].id, 'ref')
})
test('late channel response cannot replace a newer web session, even on return to same id', async () => {
  const pending = deferred()
  const state = setup(async id => id === 'channel' ? Promise.reject({ status: 404 }) : { success: true, data: { id } }, () => pending.promise)
  const first = state.load('channel')
  await Promise.resolve(); await Promise.resolve()
  state.session_id.value = 'web'
  await state.load('web')
  state.session_id.value = 'channel'
  state.sessionLoadVersion++
  pending.resolve({ success: true, data: { session: { id: 'old' }, messages: [{ id: 'old' }] } })
  await first
  assert.equal(state.currentSession.value.id, 'web')
  assert.equal(state.batches.length, 0)
})
test('ordinary web session keeps its writable mode without calling channel API', async () => {
  let called = false
  const state = setup(async () => ({ success: true, data: { id: 'web' } }), async () => { called = true })
  state.session_id.value = 'web'
  await state.load('web')
  assert.equal(state.chatMode.value, 'web')
  assert.equal(called, false)
})
test('failed channel response leaves the conversation locked', async () => {
  const state = setup(async () => { throw Error('404') }, async () => ({ success: false }))
  await state.load('channel')
  assert.equal(state.chatMode.value, 'error')
  assert.equal(state.batches.length, 0)
  assert.equal(state.historyLoading.value, false)
})


test('other accessible IM platforms retain the existing web experience', async () => {
  let called = false
  const state = setup(async () => ({ success: true, data: { id: 'wecom', im_platform: 'wecom' } }), async () => { called = true })
  state.session_id.value = 'wecom'
  await state.load('wecom')
  assert.equal(state.chatMode.value, 'web')
  assert.equal(called, false)
})
test('accessible Octo sessions still use the restricted channel path', async () => {
  const state = setup(async () => ({ success: true, data: { id: 'channel', im_platform: 'octo' } }), async () => ({ success: true, data: { session: { id: 'channel' }, messages: [] } }))
  await state.load('channel')
  assert.equal(state.chatMode.value, 'channel')
})
test('owned Octo sessions restore the writable native chat path', async () => {
  let called = false
  const state = setup(async () => ({ success: true, data: { id: 'channel', im_platform: 'octo', can_continue: true, last_request_state: {} } }), async () => { called = true })
  await state.load('channel')
  assert.equal(state.chatMode.value, 'web')
  assert.equal(called, false)
})
test('new and embedded chats remain writable without a session lookup', async () => {
  let called = false
  const state = setup(async () => { called = true }, async () => { called = true })
  state.session_id.value = ''
  await state.load('')
  assert.equal(state.chatMode.value, 'web')
  state.chatMode.value = 'loading'
  state.props.embeddedMode = true
  await state.load('embedded')
  assert.equal(state.chatMode.value, 'web')
  assert.equal(called, false)
})
