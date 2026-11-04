import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { deleteSequentially } from './artifactBatch'

test('serially processes web and channel files and reports partial failure', async () => {
  const calls: string[] = []
  const results: string[] = []
  let active = 0
  const summary = await deleteSequentially(['web', 'channel', 'web2'], () => false, async (item) => {
    assert.equal(++active, 1)
    calls.push(item)
    await Promise.resolve()
    active--
    if (item === 'channel') throw new Error('forbidden')
  }, (item, error) => results.push(`${item}:${error ? 'failed' : 'ok'}`))
  assert.deepEqual(calls, ['web', 'channel', 'web2'])
  assert.deepEqual(results, ['web:ok', 'channel:failed', 'web2:ok'])
  assert.deepEqual(summary, { done: 3, remaining: 0 })
})

test('stopping waits for the current request and leaves later items untouched', async () => {
  let stop = false
  const results: number[] = []
  const summary = await deleteSequentially([1, 2, 3], () => stop, async () => { stop = true }, item => results.push(item))
  assert.deepEqual(results, [1])
  assert.deepEqual(summary, { done: 1, remaining: 2 })
})
