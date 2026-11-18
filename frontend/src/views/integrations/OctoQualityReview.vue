<template>
  <section class="quality">
    <div class="toolbar">
      <label>Octo 群或子区
        <t-select v-model="scopeID" :options="scopeOptions" :loading="scopesLoading" filterable
          placeholder="选择要复核的区域" />
      </label>
      <label>筛选
        <t-select v-model="filter" :options="filters" />
      </label>
      <label>知识库<t-select v-model="filterLibrary" :options="libraryOptions" clearable /></label>
      <label>从<t-input v-model="from" type="date" /></label>
      <label>到<t-input v-model="to" type="date" /></label>
      <label>关键词<t-input v-model="keyword" clearable @enter="applyFilters" /></label>
      <label>主题<t-input v-model="topic" clearable @enter="applyFilters" /></label>
      <t-button variant="outline" :loading="loading" :disabled="!scopeID" @click="applyFilters">应用筛选</t-button>
      <t-button variant="outline" @click="openSettings">后台复核设置</t-button>
    </div>
    <p v-if="sample" class="hint">
      {{ day(sample.since) }} 至 {{ day(sample.until) }} · 此筛选下 {{ sample.total }} 条文字问答
      <span v-if="sample.truncated"> · 窗口超出读取上限，可缩短日期以核对完整数据</span>
      <t-button v-if="group" size="small" variant="text" @click="group = ''; applyFilters()">清除问题组</t-button>
    </p>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <div v-if="overview && sample" class="metrics">
      <section v-for="metric in metrics" :key="metric.label" class="turn">
        <span class="hint">{{ metric.label }}</span><strong>{{ metric.value }}</strong>
        <router-link :to="drillDown(metric.filter)">查看对应问答</router-link>
      </section>
      <section class="turn"><h3>每日提问</h3>
        <div v-for="bucket in sample.stats.days" :key="bucket.label" class="trend">
          <span>{{ bucket.label }}</span><meter :value="bucket.count" :max="trendMax" /><span>{{ bucket.count }}</span>
        </div>
      </section>
      <section class="turn"><h3>业务主题</h3>
        <router-link v-for="bucket in sample.stats.topics.slice(0, 12)" :key="bucket.label" :to="drillDown('all', '', bucket.label)">{{ bucket.label }} · {{ bucket.count }} 条</router-link>
      </section>
      <section class="turn"><h3>高频问题</h3>
        <router-link v-for="bucket in sample.stats.groups.slice(0, 12)" :key="bucket.id" :to="drillDown('all', bucket.id)">{{ bucket.question }} · {{ bucket.count }} 次 / {{ bucket.users }} 人<span v-if="bucket.gaps"> · 疑似未答 {{ bucket.gaps }} 次</span></router-link>
      </section>
      <section v-if="sample.stats.similar?.length" class="turn">
        <h3>相似问法待核对</h3>
        <div v-for="pair in sample.stats.similar.slice(0, 6)" :key="pair.a.id + pair.b.id" class="turn">
          <router-link :to="drillDown('all',pair.a.id)">{{ pair.a.question }} · {{ pair.a.count }} 次</router-link>
          <router-link :to="{...drillDown('all',pair.b.id),query:{...drillDown('all',pair.b.id).query,merge:pair.a.example_id,mergeLabel:pair.a.question}}">
            {{ pair.b.question }} · {{ pair.b.count }} 次
          </router-link>
        </div>
      </section>
    </div>
    <t-loading v-if="!overview" :loading="loading">
      <div v-if="visibleRows.length" class="row">
        <t-checkbox :checked="checked.length === visibleRows.length" @change="checkPage">选择本页</t-checkbox>
        <t-button size="small" variant="outline" :disabled="!checked.length" @click="openBatch">批量操作 {{ checked.length || '' }}</t-button>
      </div>
      <t-empty v-if="!visibleRows.length" :description="scopeID ? '暂无符合筛选的问答' : '选择一个 Octo 区域查看问答'" />
      <article v-for="row in visibleRows" :key="row.id" class="turn">
        <div class="row">
          <t-checkbox :checked="checked.includes(row.id)" aria-label="选择问答" @change="toggleRow(row.id)" />
          <t-tag size="small" variant="light" :theme="row.outcome === 'answered' ? 'default' : 'warning'">
            {{ outcomes[row.outcome] || row.outcome }}
          </t-tag>
          <span v-if="row.unanswered_repeats > 1">同一问法疑似未答 {{ row.unanswered_repeats }} 次</span>
          <span v-if="row.unavailable_sources">{{ row.unavailable_sources }} 条资料线索当前不可读取</span>
          <span>{{ new Date(row.created_at).toLocaleString() }}</span>
          <t-tag v-if="row.review?.suggestion" size="small" variant="light">{{ suggestions[row.review.suggestion] }}</t-tag>
          <span v-if="row.review?.disposition">{{ dispositions[row.review.disposition] }}</span>
          <span v-if="row.review?.topic">{{ row.review.topic }}</span>
          <span v-if="row.review?.issue_id">已关联问题</span>
        </div>
        <h3>{{ row.question }}</h3>
        <OctoMarkdown :text="row.answer" compact empty="没有送达的 Bot 答复" />
        <p v-if="row.review?.reason" class="hint">{{ row.review.reason }}</p>
        <div class="row">
          <span class="hint">{{ row.sources?.length ? `${row.sources.length} 条可核查资料线索` : '无结构化资料记录' }}</span>
          <t-button size="small" variant="outline" @click="openRow(row)">查看和复核</t-button>
        </div>
      </article>
      <t-pagination v-if="(sample?.total ?? 0) > pageSize" v-model:current="page" :total="sample?.total ?? 0"
        :page-size="pageSize" :show-page-size="false" />
    </t-loading>

    <t-dialog v-model:visible="batchOpen" header="批量处理问答" :confirm-btn="{content:'应用',loading:batchBusy}" @confirm="runBatch">
      <div class="review">
        <p>已选 {{ checked.length }} 条问答。</p>
        <t-select v-model="batchAction" :options="batchActions" />
        <t-select v-if="batchAction === 'review'" v-model="batchValue" :options="libraryOptions" placeholder="选择证据所属知识库" />
        <t-select v-if="batchAction === 'disposition'" v-model="batchValue" :options="dispositionOptions" />
        <t-select v-if="batchAction === 'link'" v-model="batchValue" :options="issueOptions" filterable />
        <t-select v-if="batchAction === 'merge'" v-model="batchValue" :options="mergeOptions" filterable />
        <t-input v-if="batchAction === 'topic'" v-model="batchValue" placeholder="业务主题" :maxlength="48" />
        <p v-if="batchMessage" role="status">{{ batchMessage }}</p>
      </div>
    </t-dialog>
    <t-dialog v-model:visible="settingsOpen" header="后台复核设置" :confirm-btn="{content:'保存',loading:settingsBusy}" @confirm="persistSettings">
      <div v-if="settings" class="review">
        <label>自动复核<t-switch v-model="settings.enabled" /></label>
        <label>复核模型<t-select v-model="settings.model_id" :options="modelOptions" clearable placeholder="沿用对应渠道的模型" /></label>
        <label>每批上限<t-input-number v-model="settings.per_run" :min="1" :max="12" /></label>
        <label>每日上限<t-input-number v-model="settings.daily_limit" :min="1" :max="500" /></label>
        <label>普通答复抽检百分比<t-input-number v-model="settings.sample_percent" :min="0" :max="100" /></label>
      </div>
      <p v-if="settingsError" role="status">{{ settingsError }}</p>
    </t-dialog>

    <t-drawer v-model:visible="drawer" attach="body" header="问答复核" size="min(750px, 100vw)" :footer="false">
      <div v-if="selected" class="review">
        <section>
          <h3>用户提问</h3>
          <OctoMarkdown :text="selected.question" />
          <h3>Bot 答复</h3>
          <OctoMarkdown :text="selected.answer" empty="没有送达的 Bot 答复" />
        </section>
        <label>负责核查的知识库
          <t-select v-model="libraryID" :options="libraryOptions" :disabled="analyzing || confirming"
            placeholder="选择该问题对应的库" />
        </label>
        <div class="row">
          <t-button v-if="selected.review?.state === 'done'" :disabled="!libraryID || analyzing" @click="analyze(true)">查看已复核证据</t-button>
          <t-button :loading="analyzing" :disabled="!libraryID || confirming" @click="analyze(false)">用 AI 复核</t-button>
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
import { useRoute } from 'vue-router'
import { listScopes, type OctoScope } from '@/api/octo'
import { listModels } from '@/api/model'
import { scopePath } from './octoBusinessView'
import OctoMarkdown from './OctoMarkdown.vue'
import {
  analyzeQuality, batchQuality, confirmQuality, listContacts, listIssues, listQuality, qualitySettings, saveQualitySettings,
  type OctoContact, type OctoIssue, type QualityDraft, type QualityPage, type QualityRow,
  type QualitySettings,
} from '@/api/octo-business'

const props = defineProps<{overview?:boolean}>()
const route = useRoute()
const param = (key: string) => typeof route.query[key] === 'string' ? String(route.query[key]) : ''
const outcomes: Record<string, string> = {
  answered: '有答复', suspected_gap: '疑似未答', failed: '处理失败', retrieval_warning: '检索遇到异常',
}
const categories: Record<string, string> = {
  missing: '疑似资料缺口', citation: '疑似引用问题', outdated: '资料需核查',
  technical: '技术或检索问题', adequate: '现有答复可用', uncertain: '证据不足',
}
const filters = [
  { value: 'attention', label: '待关注' }, { value: 'all', label: '全部问答' }, { value: 'repeated', label: '反复疑似未答' },
  { value: 'failed', label: '处理或检索异常' }, { value: 'unavailable', label: '资料当前不可读取' },
  { value: 'sources', label: '带资料线索' }, { value: 'linked', label: '已关联问题' },
  { value: 'reviewed', label: '已复核' },
]
const suggestions: Record<string, string> = {process:'建议处理',watch:'建议观察',dismiss:'建议无需处理',uncertain:'证据不足'}
filters.push(...Object.entries(suggestions).map(([value,label]) => ({value,label})))
const dispositions: Record<string, string> = {seen:'已阅',watch:'观察',dismiss:'无需处理'}
const scopeID = ref(param('scope'))
const filter = ref(param('filter') || (props.overview ? 'all' : 'attention'))
const filterLibrary = ref(param('library'))
const keyword = ref(param('keyword'))
const topic = ref(param('topic'))
const group = ref(param('group'))
const from = ref(param('from'))
const to = ref(param('to'))
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
const visibleRows = computed(() => sample.value?.rows ?? [])
const day = (s: string) => new Date(s).toLocaleDateString('zh-CN', {timeZone:'Asia/Shanghai'})
const trendMax = computed(() => Math.max(1, ...(sample.value?.stats.days ?? []).map(b => b.count)))
const metrics = computed(() => {
  const stats = sample.value?.stats
  return stats ? [
    {label:'提问量',value:stats.questions,filter:'all'}, {label:'提问人数',value:stats.users,filter:'all'},
    {label:'待关注',value:stats.needs_review,filter:'attention'}, {label:'已复核',value:stats.reviewed,filter:'reviewed'},
    {label:'资料不可用',value:stats.unavailable,filter:'unavailable'}, {label:'处理或检索异常',value:stats.failed,filter:'failed'},
  ] : []
})
const drillDown = (outcome:string, id = '', label = topic.value) => ({
  name:'knowledgeQuality',
  query:{scope:scopeID.value,filter:outcome,library:filterLibrary.value,keyword:keyword.value,topic:label,group:id,from:from.value,to:to.value},
})
const checked = ref<string[]>([])
const batchOpen = ref(false)
const batchBusy = ref(false)
const batchAction = ref('disposition')
const batchValue = ref('')
const batchMessage = ref('')
const issueOptions = ref<{value:string;label:string}[]>([])
const mergeOptions = computed(() => [
  ...(param('merge') ? [{value:param('merge'),label:param('mergeLabel')}] : []),
  ...visibleRows.value.map(row => ({value:row.id,label:row.question})),
])
const dispositionOptions = [...Object.entries(dispositions).map(([value,label]) => ({value,label})), {value:'',label:'恢复未处置'}]
const batchActions = [
  {value:'disposition',label:'人工处置'},{value:'review',label:'加入复核队列'},{value:'topic',label:'设置主题'},
  {value:'link',label:'关联已有问题'},{value:'merge',label:'合并到所选问答的组'},{value:'split',label:'拆为新组'},
]
const settingsOpen = ref(false)
const settingsBusy = ref(false)
const settings = ref<QualitySettings>()
const settingsError = ref('')
const modelOptions = ref<{value:string;label:string}[]>([])
function toggleRow(id: string) {
  checked.value = checked.value.includes(id) ? checked.value.filter(item => item !== id) : [...checked.value, id]
}
function checkPage() {
  checked.value = checked.value.length === visibleRows.value.length ? [] : visibleRows.value.map(row => row.id)
}
async function openBatch() {
  batchMessage.value = ''
  batchAction.value = 'disposition'
  batchValue.value = 'seen'
  issueOptions.value = []
  batchOpen.value = true
  try {
    const response = await listIssues({scope_id:scopeID.value,page_size:100})
    if (batchOpen.value) issueOptions.value = response.data.items.map(issue => ({value:issue.id,label:issue.title}))
  } catch { /* The other batch actions remain available. */ }
}
async function runBatch() {
  if (batchBusy.value || !checked.value.length || (['review','link','merge'].includes(batchAction.value) && !batchValue.value)) return
  batchBusy.value = true
  try {
    const response = await batchQuality({
      scope_id:scopeID.value, message_ids:checked.value, action:batchAction.value, value:batchValue.value,
      library_id:batchAction.value === 'review' ? batchValue.value : undefined,
    })
    const accepted = response.data.filter(item => item.state === 'updated').length
    if (accepted === response.data.length) batchOpen.value = false
    else batchMessage.value = `成功 ${accepted} 条，另有 ${response.data.length - accepted} 条需要核对证据或权限。`
    await reload()
  } catch { batchMessage.value = '操作无法完成，请检查所选证据和访问权限。' }
  finally { batchBusy.value = false }
}
async function openSettings() {
  settingsOpen.value = true
  settingsBusy.value = true
  settingsError.value = ''
  try {
    const [response, models] = await Promise.all([qualitySettings(),listModels('KnowledgeQA')])
    settings.value = response.data
    modelOptions.value = models.flatMap(model => model.id ? [{value:model.id,label:model.display_name || model.name}] : [])
  } catch { settingsError.value = '配置读取失败。' }
  finally { settingsBusy.value = false }
}
async function persistSettings() {
  if (!settings.value || settingsBusy.value) return
  settingsBusy.value = true
  try {
    await saveQualitySettings({...settings.value,model_id:settings.value.model_id || ''})
    settingsOpen.value = false
  } catch { settingsError.value = '保存失败。请检查已配置的模型、每批上限及每日预算。' }
  finally { settingsBusy.value = false }
}

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
    const stop = to.value ? new Date(new Date(`${to.value}T00:00:00+08:00`).getTime() + 86400000).toISOString() : undefined
    const response = await listQuality(scopeID.value,{
      filter:filter.value, keyword:keyword.value, topic:topic.value, group:group.value, library_id:filterLibrary.value,
      from:from.value ? `${from.value}T00:00:00+08:00` : undefined, to:stop, page:page.value, page_size:pageSize,
    })
    if (version === listVersion) { sample.value = response.data; checked.value = [] }
  } catch {
    if (version === listVersion) error.value = '读取问答失败，请检查区域和访问权限。'
  } finally {
    if (version === listVersion) loading.value = false
  }
}
function applyFilters() {
  if (page.value === 1) void reload()
  else page.value = 1
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
  libraryID.value = row.review?.library_id || (libraryOptions.value.length === 1 ? libraryOptions.value[0].value : '')
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
async function analyze(useReview: boolean) {
  const row = selected.value
  if (!row || !libraryID.value || analyzing.value || confirming.value) return
  clearDraft()
  const version = reviewVersion
  const similar = (sample.value?.rows ?? []).filter(other =>
    other.id !== row.id && other.group_key === row.group_key && other.outcome === 'suspected_gap')
  analyzing.value = true
  try {
    const response = await analyzeQuality({
      scope_id: scopeID.value, library_id: libraryID.value,
      message_ids: useReview ? [row.id] : [row.id, ...similar.slice(0, 5).map(r => r.id)], use_review:useReview,
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
  checked.value = []
  filterLibrary.value = ''
  group.value = ''
  applyFilters()
})
watch(filter, applyFilters)
watch(page, reload)
watch(batchAction, action => { batchValue.value = action === 'disposition' ? 'seen' : action === 'merge' ? param('merge') : '' })
watch(libraryID, value => { clearDraft(); void loadOwnerContacts(value) })
watch(drawer, value => { if (!value) clearDraft() })
onBeforeUnmount(() => { listVersion++; reviewVersion++; contactsVersion++ })
onMounted(async () => {
  if (scopeID.value) void reload()
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
  border-radius: var(--app-radius-md);
}
h3, p, blockquote { margin: 0; }
.turn { display: grid; gap: 8px; }
.hint, small { color: var(--td-text-color-secondary); }
.metrics { display:grid; grid-template-columns:repeat(auto-fit, minmax(210px, 1fr)); gap:12px; }
.metrics strong { font-size:24px; }
.trend { display:grid; grid-template-columns:90px 1fr auto; gap:8px; align-items:center; }
.trend meter { width:100%; }
.review p, h3 { white-space: pre-wrap; overflow-wrap: anywhere; }
blockquote { border-left: 2px solid var(--td-component-border); padding-left: 12px; }
:deep(.t-drawer__body) { overflow-y: auto; }
</style>
