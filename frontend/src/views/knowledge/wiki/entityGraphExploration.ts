import type { WikiGraphData } from '@/api/wiki'

const relationPalette = [
  '#006bc7', '#b55008', '#764fca', '#09816b', '#bc3150', '#4d6e1c',
  '#a54792', '#007b95', '#735230', '#826000', '#3c62ad', '#a32d73',
]

export function entityRelationColor(type: string) {
  let hash = 0
  for (const char of type) hash = (Math.imul(hash, 31) + char.codePointAt(0)!) >>> 0
  return relationPalette[hash % relationPalette.length]!
}

export const entityMixedRelationColor = '#626f83'

export type EntityFrontierProbe = { graph: WikiGraphData; complete: boolean }

/** Confirm an expansion only if at least one authorized relation is still absent. */
export function entityFrontierInfo(canvas: WikiGraphData, probes: ReadonlyMap<string, EntityFrontierProbe>) {
  const visible = new Set(canvas.edges.flatMap(edge => edge.relations?.map(r => r.id) ?? []))
  const expandable = new Set<string>()
  let unknown = 0
  for (const node of canvas.nodes) {
    if (node.slug === canvas.meta.center) continue
    const probe = probes.get(node.slug)
    if (!probe) {
      unknown++
      continue
    }
    if (probe.graph.edges.some(e => (e.source === node.slug || e.target === node.slug) &&
      e.relations?.some(r => !visible.has(r.id)))) {
      expandable.add(node.slug)
    } else if (!probe.complete) {
      unknown++
    }
  }
  return { expandable, unknown }
}

/** Reuse validated results for the requested anchors and preserve directions and types. */
export function collectEntityExpansion(
  canvas: WikiGraphData, probes: ReadonlyMap<string, EntityFrontierProbe>, anchors: string[],
): WikiGraphData {
  const known = new Set(canvas.edges.flatMap(edge => edge.relations?.map(r => r.id) ?? []))
  const edges: WikiGraphData['edges'] = []
  const nodes = new Map<string, WikiGraphData['nodes'][number]>()
  for (const anchor of anchors) {
    const probe = probes.get(anchor)
    if (!probe) continue
    const byKey = new Map(probe.graph.nodes.map(node => [node.slug, node]))
    for (const edge of probe.graph.edges) {
      if (edge.source !== anchor && edge.target !== anchor) continue
      const relations = edge.relations?.filter(relation => !known.has(relation.id))
      if (!relations?.length) continue
      const source = byKey.get(edge.source)
      const target = byKey.get(edge.target)
      if (!source || !target) continue
      relations.forEach(relation => known.add(relation.id))
      edges.push({ source: edge.source, target: edge.target, relations })
      nodes.set(source.slug, source)
      nodes.set(target.slug, target)
    }
  }
  return { nodes: [...nodes.values()], edges, meta: { ...canvas.meta } }
}
