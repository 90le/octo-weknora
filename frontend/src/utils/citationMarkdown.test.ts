import test from 'node:test'
import assert from 'node:assert/strict'
import { preprocessCitationTags } from './citationMarkdown'

test('source citation attributes decode once and keep local file parameters',()=>{
 const html=preprocessCitationTags('<web url="/platform/knowledge-bases/kb?source_id=source&amp;source_path=src%2Fmain.py&amp;source_line=2" title="src/main.py:2-5" />')
 assert.ok(html.includes('source_id=source&amp;source_path=src%2Fmain.py&amp;source_line=2'))
 assert.ok(!html.includes('&amp;amp;'))
 assert.ok(html.includes('>main.py:2-5</span>'))
})
test('decoded citation attributes cannot introduce executable URLs',()=>{
 assert.equal(preprocessCitationTags('<web url="javascript&#58;alert(1)" title="unsafe" />'),'')
 assert.equal(preprocessCitationTags('<web url="//outside.example/file" title="unsafe" />'),'')
})
