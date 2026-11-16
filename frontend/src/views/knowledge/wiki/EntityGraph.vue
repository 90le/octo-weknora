<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { post } from '@/utils/request'
import { buildGraphLayout, stepGraphLayout, graphNodeRadius, graphPointBounds, type ForceNode } from './wikiGraphLayout'

interface Entity { id: string; name: string; knowledge_id: string; attributes: string[]; source_chunk_ids: string[] }
interface Relation { id: string; source_id: string; target_id: string; type: string; source_chunk_ids: string[] }
interface Source { id: string; knowledge_id: string; title: string; content: string; chunk_index: number; content_truncated: boolean }
interface Result { entities: Entity[]; relationships: Relation[]; source_chunks: Source[]; meta: { truncated: boolean } }
const props = defineProps<{ knowledgeBaseId: string }>()
const emit = defineEmits<{ (event: 'open-source-doc', id: string): void }>()
const query = ref('')
const loading = ref(false)
const searched = ref(false)
const error = ref('')
const result = ref<Result | null>(null)
const selected = ref<Entity | Relation | null>(null)
const scale = ref(1)
const pan = ref({ x: 0, y: 0 })
let dragging: { pointer: number; x: number; y: number; panX: number; panY: number; width: number; height: number } | null = null
let requestNumber = 0
watch(() => props.knowledgeBaseId, () => {
  requestNumber++
  query.value = ''; result.value = null; selected.value = null; searched.value = false; error.value = ''; loading.value = false
})

async function search() {
  const q = query.value.trim()
  if (!q || loading.value) return
  const current = ++requestNumber
  loading.value = true; error.value = ''; selected.value = null
  try {
    const response = await post<{ data: Result }>(`/api/v1/knowledge-bases/${encodeURIComponent(props.knowledgeBaseId)}/graph-search`, { query: q })
    if (current !== requestNumber) return
    result.value = response.data; searched.value = true; fit()
  } catch {
    if (current !== requestNumber) return
    result.value = null; searched.value = true; error.value = '图谱查询失败，请检查图谱服务和知识库访问权限后重试。'
  } finally {
    if (current === requestNumber) loading.value = false
  }
}

const graph = computed(() => {
  const entities = result.value?.entities ?? []
  const relations = result.value?.relationships ?? []
  const edges = relations.map(r => ({ source: r.source_id, target: r.target_id }))
  const degree = new Map<string, number>()
  edges.forEach(e => { degree.set(e.source, (degree.get(e.source) ?? 0) + 1); degree.set(e.target, (degree.get(e.target) ?? 0) + 1) })
  const inputs = entities.map(e => ({ slug: e.id, title: e.name, page_type: 'entity', link_count: degree.get(e.id) ?? 0 }))
  const layout = buildGraphLayout(inputs, edges, 900, 600)
  const nodes: ForceNode[] = inputs.map(n => {
    const p = layout.placements.get(n.slug) ?? { x: 450, y: 300 }
    return { slug: n.slug, x: p.x, y: p.y, vx: 0, vy: 0, linkCount: n.link_count, pinned: false }
  })
  for (let i = 0; i < 100; i++) stepGraphLayout(nodes, edges, layout.placements, 1 - i / 110)
  const positions = new Map(nodes.map(n => [n.slug, n]))
  const bounds = graphPointBounds(nodes)
  return { positions, bounds }
})
const viewBox = computed(() => {
  const b = graph.value.bounds
  const w = Math.max(400, b.right - b.left + 180) / scale.value
  const h = Math.max(300, b.bottom - b.top + 160) / scale.value
  return `${(b.left + b.right - w) / 2 + pan.value.x} ${(b.top + b.bottom - h) / 2 + pan.value.y} ${w} ${h}`
})
function fit() { scale.value = 1; pan.value = { x: 0, y: 0 } }
function startPan(event: PointerEvent) {
  if (event.button !== 0 || (event.target as Element).closest('.node, .edge')) return
  const svg = event.currentTarget as SVGSVGElement
  const rect = svg.getBoundingClientRect()
  if (!rect.width || !rect.height) return
  const b = graph.value.bounds
  dragging = { pointer: event.pointerId, x: event.clientX, y: event.clientY, panX: pan.value.x, panY: pan.value.y,
    width: Math.max(400, b.right - b.left + 180) / scale.value / rect.width,
    height: Math.max(300, b.bottom - b.top + 160) / scale.value / rect.height }
  svg.setPointerCapture(event.pointerId)
}
function movePan(event: PointerEvent) {
  if (!dragging || event.pointerId !== dragging.pointer) return
  pan.value = { x: dragging.panX - (event.clientX - dragging.x) * dragging.width,
    y: dragging.panY - (event.clientY - dragging.y) * dragging.height }
}
function endPan(event: PointerEvent) {
  if (dragging?.pointer === event.pointerId) dragging = null
}
const names = computed(() => new Map((result.value?.entities ?? []).map(e => [e.id, e.name])))
const selectedSources = computed(() => {
  const ids = new Set(selected.value?.source_chunk_ids ?? [])
  return (result.value?.source_chunks ?? []).filter(s => ids.has(s.id))
})
function isRelation(value: Entity | Relation): value is Relation { return 'source_id' in value }
</script>

<template>
  <section class="entity-graph">
    <form class="search" @submit.prevent="search">
      <t-input v-model="query" clearable placeholder="输入实体名称，例如产品名或人物" aria-label="搜索实体" />
      <t-button type="submit" theme="primary" :loading="loading" :disabled="!query.trim()">搜索实体</t-button>
    </form>
    <p class="hint">搜索实体并展示相关的一跳关系；图谱来自已有抽取结果，不会重新处理文档。</p>
    <p v-if="error" role="alert" class="hint error">{{ error }}</p>
    <p v-else-if="!searched" class="hint">输入名称开始查看实体关系。</p>
    <p v-else-if="!result?.entities.length" class="hint">没有找到可访问的匹配实体，请尝试其他名称。</p>
    <template v-if="result?.entities.length">
      <p class="hint">显示 {{ result.entities.length }} 个实体、{{ result.relationships.length }} 条关系。{{ result.meta.truncated ? '结果已达到显示上限，请缩小搜索范围。' : '' }}</p>
      <div class="graph-body">
        <div class="canvas-wrap">
          <div class="zoom"><button type="button" @click="scale = Math.min(3, scale * 1.25)" aria-label="放大">＋</button><button type="button" @click="scale = Math.max(.4, scale / 1.25)" aria-label="缩小">－</button><button type="button" @click="fit">适应</button></div>
          <svg :viewBox="viewBox" role="img" aria-label="实体关系图" @pointerdown="startPan" @pointermove="movePan"
            @pointerup="endPan" @pointercancel="endPan" @wheel.prevent="scale = Math.max(.4, Math.min(3, scale * ($event.deltaY > 0 ? .92 : 1.08)))">
            <defs><marker id="entity-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#8e9aab" /></marker></defs>
            <g v-for="relation in result.relationships" :key="relation.id" class="edge" role="button" tabindex="0"
              :aria-label="`${names.get(relation.source_id)} ${relation.type} ${names.get(relation.target_id)}`" @click="selected = relation" @keydown.enter="selected = relation">
              <template v-if="graph.positions.get(relation.source_id) && graph.positions.get(relation.target_id)">
                <line :x1="graph.positions.get(relation.source_id)!.x" :y1="graph.positions.get(relation.source_id)!.y" :x2="graph.positions.get(relation.target_id)!.x" :y2="graph.positions.get(relation.target_id)!.y" stroke="transparent" stroke-width="18" />
                <line :x1="graph.positions.get(relation.source_id)!.x" :y1="graph.positions.get(relation.source_id)!.y" :x2="graph.positions.get(relation.target_id)!.x" :y2="graph.positions.get(relation.target_id)!.y" stroke="#8e9aab" stroke-width="1.5" marker-end="url(#entity-arrow)" />
                <text :x="(graph.positions.get(relation.source_id)!.x + graph.positions.get(relation.target_id)!.x) / 2" :y="(graph.positions.get(relation.source_id)!.y + graph.positions.get(relation.target_id)!.y) / 2 - 5" text-anchor="middle">{{ relation.type }}</text>
              </template>
            </g>
            <g v-for="entity in result.entities" :key="entity.id" class="node" role="button" tabindex="0" :aria-label="entity.name" @click="selected = entity" @keydown.enter="selected = entity">
              <template v-if="graph.positions.get(entity.id)">
                <circle :cx="graph.positions.get(entity.id)!.x" :cy="graph.positions.get(entity.id)!.y" :r="graphNodeRadius(graph.positions.get(entity.id)!.linkCount)" fill="#2ba471" stroke="white" stroke-width="2" />
                <text :x="graph.positions.get(entity.id)!.x" :y="graph.positions.get(entity.id)!.y + graphNodeRadius(graph.positions.get(entity.id)!.linkCount) + 17" text-anchor="middle">{{ entity.name }}</text>
              </template>
            </g>
          </svg>
        </div>
        <aside class="details">
          <template v-if="selected">
            <h3 v-if="isRelation(selected)">{{ names.get(selected.source_id) }} → {{ selected.type }} → {{ names.get(selected.target_id) }}</h3>
            <h3 v-else>{{ selected.name }}</h3>
            <p v-if="isRelation(selected)" class="hint">以下为两端实体关联的来源片段，关系未必在每个片段中直接出现，请核对原文。</p>
            <p v-for="attribute in !isRelation(selected) ? selected.attributes : []" :key="attribute">{{ attribute }}</p>
            <h4>相关来源</h4>
            <article v-for="source in selectedSources" :key="source.id" class="source">
              <strong>{{ source.title }}</strong><small>片段 {{ source.chunk_index + 1 }}</small>
              <t-button size="small" variant="text" theme="primary" @click="emit('open-source-doc', source.knowledge_id)">查看原文</t-button>
              <p>{{ source.content }}{{ source.content_truncated ? '…' : '' }}</p>
            </article>
            <p v-if="!selectedSources.length" class="hint">没有可展示的来源片段。</p>
          </template>
          <p v-else class="hint">点击实体或关系，查看相关来源。</p>
        </aside>
      </div>
    </template>
  </section>
</template>

<style scoped>
.entity-graph{padding:16px;min-height:540px;flex:1;overflow:auto}.search{display:flex;gap:8px;max-width:560px}.hint{font-size:13px;color:var(--td-text-color-secondary)}.error{color:var(--td-error-color)}.graph-body{display:flex;gap:16px;min-height:520px}.canvas-wrap{position:relative;flex:1;min-width:0;border:1px solid var(--td-component-border);border-radius:8px;background:var(--td-bg-color-container)}svg{width:100%;height:520px}.zoom{position:absolute;right:10px;top:10px;z-index:1;display:flex;gap:4px}.zoom button{cursor:pointer}.node,.edge{cursor:pointer}.node text,.edge text{font-size:12px;fill:var(--td-text-color-primary);paint-order:stroke;stroke:var(--td-bg-color-container);stroke-width:4px;stroke-linejoin:round}.edge text{font-size:11px}.details{width:320px;max-height:520px;overflow:auto;padding:0 10px}.source{border-top:1px solid var(--td-component-border);padding:10px 0}.source small{display:block;color:var(--td-text-color-secondary)}.source p{white-space:pre-wrap;overflow-wrap:anywhere;font-size:13px}@media(max-width:900px){.graph-body{flex-direction:column}.details{width:auto}.canvas-wrap{min-height:400px}svg{height:400px}}
</style>
