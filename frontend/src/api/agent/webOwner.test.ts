import { test } from 'node:test';
import assert from 'node:assert/strict';
import { useWebOwnerBinding } from './webOwner';

test('bind and unbind require a verified status and confirm it with the server', async () => {
  let serverBound = false;
  const calls: string[] = [];
  const state = useWebOwnerBinding({
    get: async id => { calls.push(`get:${id}`); return { bound: serverBound }; },
    bind: async id => { calls.push(`bind:${id}`); serverBound = true; },
    unbind: async id => { calls.push(`unbind:${id}`); serverBound = false; },
  });
  assert.equal(await state.change('channel', true), false);
  await state.load('channel');
  assert.equal(await state.change('channel', true), true);
  assert.equal(state.bound.value, true);
  assert.equal(await state.change('channel', false), true);
  assert.equal(state.bound.value, false);
  assert.deepEqual(calls, ['get:channel', 'bind:channel', 'get:channel', 'unbind:channel', 'get:channel']);
});

test('network failures and malformed or unconfirmed responses never report success', async () => {
  let mode = 'normal';
  const state = useWebOwnerBinding({
    get: async () => {
      if (mode === 'failed-read') throw new Error('network');
      if (mode === 'invalid') return {} as { bound: boolean };
      return { bound: false };
    },
    bind: async () => { if (mode === 'failed-write') throw new Error('network'); },
    unbind: async () => {},
  });
  await state.load('channel');
  mode = 'failed-write';
  assert.equal(await state.change('channel', true), false);
  assert.equal(state.bound.value, null);
  assert.ok(state.error.value);
  mode = 'invalid';
  await state.load('channel');
  assert.equal(state.bound.value, null);
  mode = 'normal';
  await state.load('channel');
  assert.equal(await state.change('channel', true), false);
  assert.equal(state.bound.value, false);
  mode = 'failed-read';
  await state.load('channel');
  assert.equal(state.bound.value, null);
});

test('a successful write followed by a failed read leaves the outcome unknown', async () => {
  let failRead = false;
  const state = useWebOwnerBinding({
    get: async () => { if (failRead) throw new Error('network'); return { bound: false }; },
    bind: async () => { failRead = true; },
    unbind: async () => {},
  });
  await state.load('channel');
  assert.equal(await state.change('channel', true), false);
  assert.equal(state.bound.value, null);
  assert.match(state.error.value, /结果未知/);
});

test('late responses cannot affect another channel or a closed drawer', async () => {
  let resolve!: (value: { bound: boolean }) => void;
  const state = useWebOwnerBinding({
    get: id => id === 'old' ? new Promise(r => { resolve = r; }) : Promise.resolve({ bound: false }),
    bind: async () => {}, unbind: async () => {},
  });
  const pending = state.load('old');
  state.reset();
  await state.load('new');
  resolve({ bound: true });
  await pending;
  assert.equal(state.bound.value, false);
  state.reset();
  assert.equal(state.bound.value, null);
});
