import { test } from 'node:test'
import assert from 'node:assert/strict'
import { imRuntimeDisplay } from './imRuntimeDisplay'
test('configured enablement never implies a verified live connection',()=>{
  assert.equal(imRuntimeDisplay(true).label,'已启用')
  assert.equal(imRuntimeDisplay(true,{state:'initialized'}).label,'已启用')
  assert.equal(imRuntimeDisplay(true,{state:'online'}).label,'已连接')
  assert.equal(imRuntimeDisplay(false,{state:'online'}).label,'已停用')
  assert.equal(imRuntimeDisplay(true,{state:'stopped'}).theme,'danger')
  assert.equal(imRuntimeDisplay(true,{state:'reconnecting'}).theme,'warning')
})
