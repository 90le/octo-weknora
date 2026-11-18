<template>
  <section class="quality">
    <div class="toolbar">
      <label>Octo 群或子区
        <t-select v-model="scopeID" :options="scopeOptions" :loading="scopesLoading" filterable
          placeholder="选择要复核的区域" />
      </label>
      <label>问题筛选
        <t-select v-model="filter" :options="filters" />
      </label>
      <t-button variant="outline" :loading="loading" :disabled="!scopeID" @click="reload">刷新问答</t-button>
    </div>
    <p v-if="sample" class="hint">
      最近 14 天 · 已读取 {{ sample.sample_size }} 条文字问答
      <span v-if="sample.truncated"> · 仅统计最新 250 条候选记录</span>
    </p>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <t-loading :loading="loading">
      <t-empty v-if="!visibleRows.length" :description="scopeID ? '暂无符合筛选的问答' : '选择一个 Octo 区域查看问答'" />
      <article v-for="row in visibleRows" :key="row.id" class="turn">
        <div class="row">
          <t-tag size="small" variant="light" :theme="row.outcome === 'answered' ? 'default' : 'warning'">
            {{ outcomes[row.outcome] || row.outcome }}
          </t-tag>
          <span v-if="row.unanswered_repeats > 1">同一问法疑似未答 {{ row.unanswered_repeats }} 次</span>
          <span v-if="row.unavailable_sources">{{ row.unavailable_sources }} 条资料线索当前不可读取</span>
          <span>{{ new Date(row.created_at).toLocaleString() }}</span>
        </div>
        <h3>{{ row.question }}</h3>
        <p class="excerpt">{{ row.answer || '没有送达的 Bot 答复' }}</p>
        <div class="row">
          <span class="hint">{{ row.sources?.length ? `${row.sources.length} 条可核查资料线索` : '无结构化资料记录' }}</span>
          <t-button size="small" variant="outline" @click="openRow(row)">查看和复核</t-button>
        </div>
      </article>
      <t-pagination v-if="filteredRows.length > pageSize" v-model:current="page" :total="filteredRows.length"
        :page-size="pageSize" :show-page-size="false" />
    </t-loading>

    <t-drawer v-model:visible="drawer" attach="body" header="问答复核" size="min(750px, 100vw)" :footer="false">
      <div v-if="selected" class="review">
        <section>
          <h3>用户提问</h3>
          <p>{{ selected.question }}</p>
          <h3>Bot 答复</h3>
          <p>{{ selected.answer || '没有送达的 Bot 答复' }}</p>
        </section>
        <label>负责核查的知识库
          <t-select v-model="libraryID" :options="libraryOptions" :disabled="analyzing || confirming"
            placeholder="选择该问题对应的库" />
        </label>
        <div class="row">
          <t-button :loading="analyzing" :disabled="!libraryID || confirming" @click="analyze">用 AI 复核</t-button>
          <span v-if="selected.unanswered_repeats > 1" class="hint">同时核对最多 6 条同问法的疑似未答记录。</span>
        </div>
        <t-alert v-if="reviewError" theme="error">{{ reviewError }}</t-alert>
        <section v-if="draft">
          <div class="row"><t-tag variant="light">{{ categories[draft.finding.category] }}</t-tag><h3>{{ draft.finding.title }}</h3></div>
          <p>{{ draft.finding.reason }}</p>
          <h3>AI 补充建议</h3>
          <p>{{ draft.finding.suggestion }}</p>
          <h3>可核对的原文</h3>
          <blockquote v-for="item in draft.finding.evidence" :key="item.ref + item.quote">
            <small>{{ draft.excerpts.find(e => e.id === item.ref)?.label || item.ref }}</small>
            <p>{{ item.quote }}</p>
          </blockquote>
          <div v-for="source in draft.excerpts.filter(item => item.id.startsWith('S'))" :key="source.id">
            <details>
              <summary>{{ source.label }} · {{ source.status === 'current' ? '当前资料摘录' : '当前不可读取' }}</summary>
              <p v-if="source.text">{{ source.text }}</p>
            </details>
          </div>
        </section>
        <section v-if="draft?.id && !registered">
          <h3>人工确认</h3>
          <label>问题标题<t-input v-model="title" :maxlength="120" /></label>
          <label>核查结论<t-textarea v-model="note" :maxlength="1000" placeholder="填写你确认的事实和需要处理的内容" /></label>
          <label>分派给<t-select v-model="contactID" :options="contactOptions" clearable placeholder="选择联系人或暂不分派" /></label>
          <p v-if="contactsError" class="hint">联系人读取失败，仍可登记后在问题列表分派。</p>
          <t-button :loading="confirming" :disabled="analyzing || !note.trim() || !title.trim()"
            @click="confirm">确认登记与分派</t-button>
        </section>
        <t-alert v-if="registered" theme="success">
          已登记「{{ registered.title }}」{{ registered.owner_name ? `，负责人 ${registered.owner_name}` : '' }}。
          <router-link :to="{name:'knowledgeIssues'}">打开问题列表</router-link>
        </t-alert>
      </div>
    </t-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { listScopes, type OctoScope } from '@/api/octo'
import { scopePath } from './octoBusinessView'
import {
  analyzeQuality, confirmQuality, listContacts, listQuality,
  type OctoContact, type OctoIssue, type QualityDraft, type QualityPage, type QualityRow,
} from '@/api/octo-business'

const outcomes: Record<string, string> = {
  answered: '有答复', suspected_gap: '疑似未答', failed: '处理失败', retrieval_warning: '检索遇到异常',
}
const categories: Record<string, string> = {
  missing: '疑似资料缺口', citation: '疑似引用问题', outdated: '资料需核查',
  technical: '技术或检索问题', adequate: '现有答复可用', uncertain: '证据不足',
}
const filters = [
  { value: 'all', label: '全部问答' }, { value: 'repeated', label: '反复疑似未答' },
  { value: 'failed', label: '处理或检索异常' }, { value: 'unavailable', label: '资料当前不可读取' },
  { value: 'sources', label: '带资料线索' },
]
const scopeID = ref('')
const filter = ref('all')
const scopes = ref<OctoScope[]>([])
const scopesLoading = ref(false)
const sample = ref<QualityPage | null>(null)
const scopeOptions = computed(() => scopes.value
  .filter(scope => scope.verified_at && scope.sync_status === 'verified' && scope.name_source === 'octo')
  .map(scope => ({ value: scope.id, label: `${scopePath(scope, scopes.value)} · ${scope.account_id}` })))
const libraryOptions = computed(() => (sample.value?.libraries ?? []).map(k => ({ value: k.id, label: k.name })))
const error = ref('')
const loading = ref(false)
const page = ref(1)
const pageSize = 15
const filteredRows = computed(() => (sample.value?.rows ?? []).filter(row => {
  if (filter.value === 'repeated') return row.unanswered_repeats > 1 && row.outcome === 'suspected_gap'
  if (filter.value === 'failed') return row.outcome === 'failed' || row.outcome === 'retrieval_warning'
  if (filter.value === 'unavailable') return row.unavailable_sources > 0
  if (filter.value === 'sources') return Boolean(row.sources?.length)
  return true
}))
const visibleRows = computed(() => filteredRows.value.slice((page.value - 1) * pageSize, page.value * pageSize))

const drawer = ref(false)
const selected = ref<QualityRow | null>(null)
const libraryID = ref('')
const draft = ref<QualityDraft | null>(null)
const registered = ref<OctoIssue | null>(null)
const title = ref('')
const note = ref('')
const contactID = ref('')
const contacts = ref<OctoContact[]>([])
const contactsError = ref(false)
const contactOptions = computed(() => contacts.value.map(c => ({ value: c.id, label: [c.name, c.topic].filter(Boolean).join(' · ') })))
const reviewError = ref('')
const analyzing = ref(false)
const confirming = ref(false)
let listVersion = 0
let reviewVersion = 0
let contactsVersion = 0

async function reload() {
  const version = ++listVersion
  if (!scopeID.value) return
  loading.value = true
  error.value = ''
  try {
    const response = await listQuality(scopeID.value)
    if (version === listVersion) sample.value = response.data
  } catch {
    if (version === listVersion) error.value = '读取问答失败，请检查区域和访问权限。'
  } finally {
    if (version === listVersion) loading.value = false
  }
}
function clearDraft() {
  reviewVersion++
  draft.value = null
  registered.value = null
  reviewError.value = ''
  analyzing.value = false
  confirming.value = false
  note.value = ''
  contactID.value = ''
}
function openRow(row: QualityRow) {
  clearDraft()
  selected.value = row
  libraryID.value = libraryOptions.value.length === 1 ? libraryOptions.value[0].value : ''
  drawer.value = true
}
async function loadOwnerContacts(kb: string) {
  const version = ++contactsVersion
  contacts.value = []
  contactsError.value = false
  if (!kb) return
  try {
    const response = await listContacts(kb)
    if (version === contactsVersion) contacts.value = response.data
  } catch {
    if (version === contactsVersion) contactsError.value = true
  }
}
async function analyze() {
  const row = selected.value
  if (!row || !libraryID.value || analyzing.value || confirming.value) return
  clearDraft()
  const version = reviewVersion
  const similar = (sample.value?.rows ?? []).filter(other =>
    other.id !== row.id && other.group_key === row.group_key && other.outcome === 'suspected_gap')
  analyzing.value = true
  try {
    const response = await analyzeQuality({
      scope_id: scopeID.value, library_id: libraryID.value, message_ids: [row.id, ...similar.slice(0, 5).map(r => r.id)],
    })
    if (version === reviewVersion) {
      draft.value = response.data
      title.value = response.data.finding.title
    }
  } catch {
    if (version === reviewVersion) reviewError.value = '复核未生成可校验的证据。请核对知识库、模型和权限后重试。'
  } finally {
    if (version === reviewVersion) analyzing.value = false
  }
}
async function confirm() {
  const id = draft.value?.id
  if (!id || confirming.value || registered.value) return
  const version = reviewVersion
  confirming.value = true
  reviewError.value = ''
  try {
    const response = await confirmQuality(id, { title: title.value, note: note.value, contact_id: contactID.value })
    if (version === reviewVersion) registered.value = response.data
  } catch {
    if (version === reviewVersion) reviewError.value = '保存失败。若证据、授权或建议已变更，请重新复核。'
  } finally {
    if (version === reviewVersion) confirming.value = false
  }
}
watch(scopeID, () => {
  drawer.value = false
  sample.value = null
  page.value = 1
  void reload()
})
watch(filter, () => { page.value = 1 })
watch(libraryID, value => { clearDraft(); void loadOwnerContacts(value) })
watch(drawer, value => { if (!value) clearDraft() })
onBeforeUnmount(() => { listVersion++; reviewVersion++; contactsVersion++ })
onMounted(async () => {
  scopesLoading.value = true
  try {
    for (let offset = 0; ; offset += 100) {
      const response = await listScopes(offset)
      scopes.value.push(...response.data)
      if (response.data.length < 100) break
    }
  } catch { error.value = '读取 Octo 区域失败。' }
  finally { scopesLoading.value = false }
})
</script>

<style scoped>
.quality, .review { display: grid; gap: 16px; }
.toolbar, .row { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }
.toolbar label { min-width: 240px; }
.toolbar { align-items: end; }
label, .review section { display: grid; gap: 8px; }
.turn, .review section {
  padding: 16px;
  border: 1px solid var(--td-component-border);
  border-radius: 8px;
}
h3, p, blockquote { margin: 0; }
.turn { display: grid; gap: 8px; }
.hint, small { color: var(--td-text-color-secondary); }
.excerpt {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  overflow: hidden;
}
.review p, h3 { white-space: pre-wrap; overflow-wrap: anywhere; }
blockquote { border-left: 2px solid var(--td-component-border); padding-left: 12px; }
:deep(.t-drawer__body) { overflow-y: auto; }
</style>
