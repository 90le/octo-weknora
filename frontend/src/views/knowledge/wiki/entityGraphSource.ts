import { post } from '@/utils/request'
import type { WikiGraphData, WikiGraphQueryParams } from '@/api/wiki'

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

async function request(kbId: string, body: Record<string, string | number | undefined>) {
  const response = await post<{ data: GraphResult }>(`/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/graph-search`, body, { timeout: 15_000 })
  return response.data
}

function suggestion(entity: Entity) {
  return {
    value: entityKey(entity),
    label: entity.document_title ? `${entity.name} · ${entity.document_title}` : entity.name,
  }
}

export async function getEntityGraph(kbId: string, options: WikiGraphQueryParams): Promise<WikiGraphData> {
  const center = options.center
  const graph = await request(kbId, {
    mode: center ? 'focus' : 'overview',
    ...(center ? readKey(center) : {}),
    max_nodes: options.limit,
    max_relations: 1500,
  })
  const keys = new Map(graph.entities.map(entity => [entity.id, entityKey(entity)]))
  const neighbors = new Map(graph.entities.map(entity => [entity.id, new Set<string>()]))
  const seen = new Set<string>()
  const edges = graph.relationships.flatMap(edge => {
    const source = keys.get(edge.source_id)
    const target = keys.get(edge.target_id)
    if (!source || !target || source === target) return []
    const pair = `${source}→${target}`
    if (seen.has(pair)) return []
    seen.add(pair)
    neighbors.get(edge.source_id)?.add(target)
    neighbors.get(edge.target_id)?.add(source)
    return [{ source, target }]
  })
  return {
    nodes: graph.entities.map(entity => ({
      slug: entityKey(entity),
      title: entity.name,
      search_label: suggestion(entity).label,
      page_type: 'entity',
      link_count: neighbors.get(entity.id)?.size ?? 0,
    })),
    edges,
    meta: {
      mode: center ? 'ego' : 'overview', center, depth: center ? 1 : undefined,
      total: graph.entities.length, returned: graph.entities.length, truncated: graph.meta.truncated,
    },
  }
}

export async function searchGraphEntities(kbId: string, query: string, limit: number) {
  const graph = await request(kbId, { mode: 'suggest', query, max_nodes: limit })
  return graph.entities.map(suggestion)
}

export type EntityGraphDetails = {
  title: string
  attributes: string[]
  relations: { id: string; source: string; target: string; type: string }[]
  sources: SourceChunk[]
  truncated: boolean
}

export async function getEntityGraphDetails(kbId: string, key: string, related?: string): Promise<EntityGraphDetails | null> {
  const graph = await request(kbId, {
    mode: 'detail', ...readKey(key), related_name: related ? readKey(related).query : undefined,
    max_source_chunks: 50,
  })
  const from = graph.entities.find(entity => entityKey(entity) === key)
  const to = related ? graph.entities.find(entity => entityKey(entity) === related) : undefined
  if (!from || (related && (!to || !graph.relationships.length))) return null
  const entities = new Map(graph.entities.map(entity => [entity.id, entity.name]))
  return {
    title: to ? `${from.name} · ${to.name}` : from.name,
    attributes: to ? [] : from.attributes,
    relations: graph.relationships.map(edge => ({
      id: edge.id, type: edge.type, source: entities.get(edge.source_id)!, target: entities.get(edge.target_id)!,
    })),
    sources: graph.source_chunks,
    truncated: graph.meta.truncated,
  }
}
