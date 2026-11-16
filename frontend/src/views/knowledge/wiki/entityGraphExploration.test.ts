import assert from 'node:assert/strict'
import test from 'node:test'
import type { WikiGraphData } from '@/api/wiki'
import { collectEntityExpansion, entityFrontierInfo } from './entityGraphExploration.ts'

function graph(keys: string[], triples: string[][]): WikiGraphData {
  return {
    nodes: keys.map(slug => ({ slug, title: slug, page_type: 'entity', link_count: 0 })),
    edges: triples.map(([source, target, id]) => ({ source: source!, target: target!, relations: [{ id: id!, type: id! }] })),
    meta: { mode: 'ego', center: 'hub', returned: keys.length, total: keys.length, truncated: false },
  }
}

test('frontier confirms additional relations even when both endpoints are visible', () => {
  const canvas = graph(['hub', 'a', 'b', 'leaf', 'unchecked'], [['hub', 'a', '1'], ['hub', 'leaf', '2']])
  const result = graph(['hub', 'a', 'b', 'leaf'], [
    ['hub', 'a', '1'], ['hub', 'leaf', '2'], ['hub', 'a', '3'], ['a', 'b', '4'],
  ])
  const probes = new Map([
    ['a', { graph: result, complete: true }],
    ['b', { graph: result, complete: true }],
    ['leaf', { graph: result, complete: true }],
  ])
  assert.deepEqual(entityFrontierInfo(canvas, probes), { expandable: new Set(['a', 'b']), unknown: 1 })
  const added = collectEntityExpansion(canvas, probes, ['a', 'b'])
  assert.deepEqual(added.edges.flatMap(edge => edge.relations?.map(r => r.id)), ['3', '4'])
  assert.deepEqual(entityFrontierInfo({ ...canvas, edges: [...canvas.edges, ...added.edges] }, probes), {
    expandable: new Set(), unknown: 1,
  })
})

test('an incomplete probe without evidence remains unknown', () => {
  const canvas = graph(['hub', 'b'], [['hub', 'b', 'known']])
  assert.deepEqual(entityFrontierInfo(canvas, new Map([['b', { graph: canvas, complete: false }]])), {
    expandable: new Set(), unknown: 1,
  })
})
