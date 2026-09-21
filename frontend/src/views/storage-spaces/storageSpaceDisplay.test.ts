import assert from 'node:assert/strict'
import { test } from 'node:test'
import { canGrantDirectory, canRemoveDirectoryGrant, isDiscoveredSpaceRegistered, storageErrorMessage } from './storageSpaceDisplay.ts'
import type { LocalRoot, LocalSpace } from '@/api/local-source-roots'
const space: LocalSpace = {id:'s',name:'资料',path:'/source-roots/docs',status:'ready',readable:true,authorized_root_count:1,enabled_root_count:1,data_source_count:0,usage_complete:true}
const root: LocalRoot = {id:'r',name:'资料',space_id:'s',directory:'docs',tenant_id:1,enabled:true,data_source_count:0,usage_complete:true}

test('unavailable and unsafe spaces never offer a directory grant', () => {
  assert.equal(canGrantDirectory(space),true)
  assert.equal(canGrantDirectory({...space,readable:false}),false)
  assert.equal(canGrantDirectory({...space,status:'unsafe'}),false)
  assert.equal(canGrantDirectory({...space,status:'unavailable'}),false)
})
test('unknown usage is not treated as zero when removing grants', () => {
  assert.equal(canRemoveDirectoryGrant(root),true)
  assert.equal(canRemoveDirectoryGrant({...root,data_source_count:1}),false)
  assert.equal(canRemoveDirectoryGrant({...root,usage_complete:false}),false)
  assert.equal(canRemoveDirectoryGrant({...root,data_source_count:undefined}),false)
})
test('equal directory names under other paths do not hide a discoverable mount', () => {
  const entry = {name:'docs',directory:'docs'}
  assert.equal(isDiscoveredSpaceRegistered(entry,[space]),true)
  assert.equal(isDiscoveredSpaceRegistered(entry,[{...space,path:'/other/docs'}]),false)
  assert.equal(isDiscoveredSpaceRegistered(entry,[{...space,path:'/source-roots/docs-old'}]),false)
  assert.equal(isDiscoveredSpaceRegistered(entry,[{...space,path:'/source-roots/docs/'}]),true)
})
test('revoked grants and unsafe paths get useful errors, unknown errors keep the native message', () => {
  const translate = (key: string) => key
  assert.equal(storageErrorMessage({code:'unsafe_folder',message:'raw server text'},translate),'storageSpaces.unsafeDirectory')
  assert.equal(storageErrorMessage({error:{code:'folder_access_denied'}},translate),'storageSpaces.directoryDenied')
  assert.equal(storageErrorMessage({message:'Network unavailable'},translate),'Network unavailable')
  assert.equal(storageErrorMessage(null,translate),'sourceRoots.failed')
  assert.equal(storageErrorMessage({code:'__proto__',message:'Unknown error'},translate),'Unknown error')
})
