import { post } from '@/utils/request'
import type { WikiGraphData, WikiGraphQueryParams } from '@/api/wiki'

export type EntityGraphFilters = { source_document?: string; relation_type?: string }

interface Entity {
  id: string
  name: string
  knowledge_id: string
  document_title?: string
  attributes: string[]
  source_chunk_ids: string[]
}
interface Relation {
  id: string
  source_id: string
  target_id: string
  type: string
  source_chunk_ids: string[]
}
interface SourceChunk {
  id: string
  knowledge_id: string
  title: string
  content: string
  chunk_index: number
  content_truncated: boolean
}
interface GraphResult {
  entities: Entity[]
  relationships: Relation[]
  source_chunks: SourceChunk[]
  meta: { truncated: boolean }
}

function entityKey(entity: Entity) {
  return encodeURIComponent(JSON.stringify([entity.knowledge_id, entity.name]))
}

function readKey(key: string): { knowledge_id: string; query: string } {
  const [knowledge_id, query] = JSON.parse(decodeURIComponent(key))
  return { knowledge_id, query }
}

async function request(kbId: string, body: Record<string, unknown>, signal?: AbortSignal) {
  const response = await post<{ data: GraphResult }>(`/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/graph-search`, body, { timeout: 15_000, signal })
  return response.data
}

function suggestion(entity: Entity) {
  return {
    value: entityKey(entity),
    label: entity.document_title ? `${entity.name} · ${entity.document_title}` : entity.name,
  }
}

export async function getEntityGraph(
  kbId: string, options: WikiGraphQueryParams, filters?: EntityGraphFilters,
): Promise<WikiGraphData> {
  const center = options.center
  const graph = await request(kbId, {
    mode: center ? 'focus' : 'overview',
    ...(center ? readKey(center) : {}),
    max_nodes: options.limit,
    max_relations: 1500,
    ...filters,
  })
  return toCanvasGraph(graph, center)
}

export async function probeEntityFrontier(
  kbId: string, keys: string[], filters: EntityGraphFilters, signal: AbortSignal,
): Promise<WikiGraphData> {
  const anchors = keys.map(key => {
    const { knowledge_id, query } = readKey(key)
    return { knowledge_id, name: query }
  })
  return toCanvasGraph(await request(kbId, { mode: 'frontier', anchors, max_nodes: 500, max_relations: 1500, ...filters }, signal))
}

function toCanvasGraph(graph: GraphResult, center?: string): WikiGraphData {
  const keys = new Map(graph.entities.map(entity => [entity.id, entityKey(entity)]))
  const neighbors = new Map(graph.entities.map(entity => [entity.id, new Set<string>()]))
  const grouped = new Map<string, WikiGraphData['edges'][number]>()
  for (const edge of graph.relationships) {
    const source = keys.get(edge.source_id)
    const target = keys.get(edge.target_id)
    if (!source || !target || source === target) continue
    const pair = `${source}→${target}`
    const relation = { id: edge.id, type: edge.type }
    const existing = grouped.get(pair)
    if (existing) existing.relations!.push(relation)
    else grouped.set(pair, { source, target, relations: [relation] })
    neighbors.get(edge.source_id)?.add(target)
    neighbors.get(edge.target_id)?.add(source)
  }
  return {
    nodes: graph.entities.map(entity => ({
      slug: entityKey(entity),
      title: entity.name,
      search_label: suggestion(entity).label,
      page_type: 'entity',
      link_count: neighbors.get(entity.id)?.size ?? 0,
      document_id: entity.knowledge_id,
      document_title: entity.document_title || entity.knowledge_id,
    })),
    edges: [...grouped.values()],
    meta: {
      mode: center ? 'ego' : 'overview', center, depth: center ? 1 : undefined,
      total: graph.entities.length, returned: graph.entities.length, truncated: graph.meta.truncated,
    },
  }
}

export async function searchGraphEntities(kbId: string, query: string, limit: number, sourceDocument?: string) {
  const graph = await request(kbId, { mode: 'suggest', query, max_nodes: limit, source_document: sourceDocument })
  return graph.entities.map(suggestion)
}

export type EntityGraphDetails = {
  title: string
  document: { id: string; title: string }
  attributes: string[]
  relations: { id: string; source: string; target: string; type: string }[]
  sources: SourceChunk[]
  truncated: boolean
}

export async function getEntityGraphDetails(
  kbId: string, key: string, related?: string, filters?: EntityGraphFilters,
): Promise<EntityGraphDetails | null> {
  const graph = await request(kbId, {
    mode: 'detail', ...readKey(key), related_name: related ? readKey(related).query : undefined,
    max_source_chunks: 50,
    ...filters,
  })
  const from = graph.entities.find(entity => entityKey(entity) === key)
  const to = related ? graph.entities.find(entity => entityKey(entity) === related) : undefined
  if (!from || (related && (!to || !graph.relationships.length))) return null
  const entities = new Map(graph.entities.map(entity => [entity.id, entity.name]))
  return {
    title: to ? `${from.name} · ${to.name}` : from.name,
    document: { id: from.knowledge_id, title: from.document_title || from.knowledge_id },
    attributes: to ? [] : from.attributes,
    relations: graph.relationships.map(edge => ({
      id: edge.id, type: edge.type, source: entities.get(edge.source_id)!, target: entities.get(edge.target_id)!,
    })),
    sources: graph.source_chunks,
    truncated: graph.meta.truncated,
  }
}
