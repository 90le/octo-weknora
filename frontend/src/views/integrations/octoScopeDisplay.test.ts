import test from 'node:test'
import assert from 'node:assert/strict'
import type { OctoScope } from '@/api/octo'
import { groupScopes, scopeKnowledgeRows } from './octoScopeDisplay'

const scope = (id:string, group_id:string, subarea_id='', account_id='bot', display_name=id):OctoScope => ({id,group_id,subarea_id,account_id,display_name,name_source:'octo',sync_status:'verified',sync_error:'',checked_at:null,verified_at:null,inherit_parent:false,allow_knowledge_creation:false,aggregate_child_issues:false})

test('group hierarchy keeps exact account and parent identity, including search',()=>{
  const records=[scope('parent','g'),scope('child','g','1','bot','产品子区'),scope('other-account','g','','another'),scope('other-child','g','2','another')]
  const all=groupScopes(records)
  assert.deepEqual(all.map(group=>[group.scope.id,group.children.map(child=>child.id)]),[['parent',['child']],['other-account',['other-child']]])
  const found=groupScopes(records,'产品')
  assert.equal(found.length,1);assert.equal(found[0]!.scope.id,'parent');assert.equal(found[0]!.children[0]!.id,'child')
})

test('query and independent management are merged without inventing grants',()=>{
  const rows=scopeKnowledgeRows([
    {knowledge_base_id:'a',from_scope_id:'parent',inherited:true,can_manage:true},
    {knowledge_base_id:'b',from_scope_id:'current',inherited:false,can_manage:false},
  ],['b','c'],[{id:'a',name:'Inherited'},{id:'b',name:'Both'},{id:'c',name:'Manage only'}])
  assert.deepEqual(rows.map(row=>[row.knowledgeBaseId,row.query,row.managed]),[['a','inherited',false],['b','direct',true],['c','none',true]])
  assert.equal(rows[0]!.name,'Inherited')
})

test('direct binding wins once and missing names never become a fake group name',()=>{
  const rows=scopeKnowledgeRows([
    {knowledge_base_id:'a',from_scope_id:'current',inherited:false,can_manage:false},
    {knowledge_base_id:'a',from_scope_id:'parent',inherited:true,can_manage:false},
  ],[],[])
  assert.equal(rows.length,1);assert.equal(rows[0]!.query,'direct');assert.equal(rows[0]!.sourceScopeId,'current')
  assert.equal(rows[0]!.name,'知识库名称暂不可用')
})
