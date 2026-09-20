import assert from 'node:assert/strict'
import test from 'node:test'
import { csvCell,displayPerson,safeAttachmentURL } from './octoBusinessDisplay'
test('report exports untrusted text as data, not spreadsheet formulas',()=>{
  assert.equal(csvCell('=HYPERLINK("https://x")'), '"\'=HYPERLINK(""https://x"")"')
  assert.equal(csvCell('  +123'),'"\'  +123"')
  assert.equal(csvCell('plain\ntext'),'"plain\ntext"')
})
test('identity fallback remains useful without display names',()=>{assert.equal(displayPerson('','uid-1'),'uid-1');assert.equal(displayPerson('Alice','uid-1'),'Alice')})
test('attachment links reject active schemes',()=>{assert.equal(safeAttachmentURL('javascript:alert(1)'),null);assert.equal(safeAttachmentURL('https://example.test/a'),'https://example.test/a')})
