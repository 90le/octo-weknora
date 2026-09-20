import { test } from 'node:test'
import assert from 'node:assert/strict'
import { knowledgeOperationsTarget } from './knowledgeOperationsRoute'
test('existing deep links preserve their task and selected scope',()=>{
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-octo',scope:'scope-one'}),{name:'octoGroups',query:{scope:'scope-one'}})
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-im',agentId:'agent-one'}),{name:'knowledgeChannels',query:{agentId:'agent-one'}})
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-octo',view:'contacts',kbId:'kb-one'}),{name:'knowledgeContacts',params:{kbId:'kb-one'}})
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-octo',view:'schedule'}),{name:'knowledgeReports'})
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-octo',view:'issues'}),{name:'knowledgeIssues'})
})
test('unrelated settings stay native and ambiguous query parameters do not choose assets',()=>{
  assert.equal(knowledgeOperationsTarget({section:'models'}),null)
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-octo',view:'contacts',kbId:['a','b']}),{name:'knowledgeBaseList'})
  assert.deepEqual(knowledgeOperationsTarget({section:'integration-octo',scope:['a','b']}),{name:'octoGroups',query:{}})
})
