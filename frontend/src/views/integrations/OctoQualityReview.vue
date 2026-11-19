<template>
  <section class="quality">
    <form class="filters" @submit.prevent="applyFilters()">
      <div class="toolbar">
        <label>Octo 群或子区
          <t-select v-model="scopeID" :options="scopeOptions" :loading="scopesLoading" filterable
            placeholder="选择要复核的区域" />
        </label>
        <label>问答范围
          <t-select v-model="filter" :options="filters" />
        </label>
        <label>知识库<t-select v-model="filterLibrary" :options="libraryOptions" placeholder="全部授权库" clearable /></label>
        <div class="row filter-actions">
          <t-button type="submit" :loading="loading" :disabled="!scopeID">应用筛选</t-button>
          <t-button v-if="!overview" variant="text" @click="openSettings">后台复核设置</t-button>
        </div>
      </div>
      <details class="extra-filters" :open="Boolean(param('from') || param('to') || param('keyword') || param('topic'))">
        <summary>日期、关键词和主题</summary>
        <div class="toolbar advanced">
          <label>开始日期<t-input v-model="from" type="date" /></label>
          <label>结束日期<t-input v-model="to" type="date" /></label>
          <label>关键词<t-input v-model="keyword" clearable /></label>
          <label>主题<t-input v-model="topic" clearable /></label>
        </div>
      </details>
    </form>
    <p v-if="sample" class="hint">
      {{ day(sample.since) }} 至 {{ day(sample.until) }} · 此筛选下 {{ sample.total }} 条文字问答
      <span v-if="sample.truncated"> · 窗口超出读取上限，可缩短日期以核对完整数据</span>
      <t-button v-if="group" size="small" variant="text" @click="group = ''; applyFilters()">清除问题组</t-button>
    </p>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <template v-if="overview && sample">
      <div class="metrics">
        <router-link v-for="metric in metrics" :key="metric.label" class="metric" :to="drillDown(metric.filter)">
          <span>{{ metric.label }}</span><strong>{{ metric.value }}</strong>
        </router-link>
      </div>
      <div class="overview-panels">
        <section class="turn"><h3>每日提问</h3>
          <div v-for="bucket in sample.stats.days" :key="bucket.label" class="trend">
            <span>{{ bucket.label }}</span><meter :value="bucket.count" :max="trendMax" :aria-label="`${bucket.label} 提问 ${bucket.count} 条`" /><span>{{ bucket.count }}</span>
          </div>
          <p v-if="!sample.stats.days.length" class="hint">所选日期内暂无问答。</p>
        </section>
        <section class="turn"><h3>高频问题</h3>
          <router-link v-for="bucket in frequentGroups" :key="bucket.id" class="ranking" :to="drillDown('all', bucket.id)">
            <span>{{ bucket.question }}<small v-if="bucket.gaps">疑似未答 {{ bucket.gaps }} 次</small></span>
            <span class="hint">{{ bucket.count }} 次 · {{ bucket.users }} 人</span>
          </router-link>
          <p v-if="!frequentGroups.length" class="hint">所选范围内暂无重复提问。</p>
        </section>
        <section class="turn"><h3>业务主题</h3>
          <router-link v-for="bucket in sample.stats.topics.slice(0, 8)" :key="bucket.label" class="ranking" :to="drillDown('all', '', bucket.label)"><span>{{ bucket.label }}</span><span class="hint">{{ bucket.count }} 条</span></router-link>
          <p v-if="!sample.stats.topics.length" class="hint">所选问答暂无主题标签。</p>
        </section>
      </div>
      <details v-if="sample.stats.similar?.length" class="turn similar">
        <summary>相似问法待核对 · {{ sample.stats.similar.length }} 组</summary>
        <div v-for="pair in sample.stats.similar" :key="pair.a.id + pair.b.id" class="similar-pair">
          <router-link :to="drillDown('all',pair.a.id)">{{ pair.a.question }} · {{ pair.a.count }} 次</router-link>
          <router-link :to="{...drillDown('all',pair.b.id),query:{...drillDown('all',pair.b.id).query,merge:pair.a.example_id,mergeLabel:pair.a.question}}">
            {{ pair.b.question }} · {{ pair.b.count }} 次
          </router-link>
        </div>
      </details>
    </template>
    <t-empty v-else-if="overview && !loading && !error" description="选择一个 Octo 群或子区查看总览" />
    <t-loading v-if="!overview" :loading="loading">
      <div v-if="visibleRows.length" class="row selection">
        <t-checkbox :checked="checked.length === visibleRows.length" :indeterminate="checked.length > 0 && checked.length < visibleRows.length" @change="checkPage">选择本页</t-checkbox>
        <t-button v-if="checked.length" size="small" variant="outline" @click="openBatch">批量操作 · {{ checked.length }} 条</t-button>
      </div>
      <t-empty v-if="!visibleRows.length" :description="scopeID ? '暂无符合筛选的问答' : '选择一个 Octo 区域查看问答'" />
      <div v-if="visibleRows.length" class="queue">
        <article v-for="row in visibleRows" :key="row.id" class="queue-row">
          <t-checkbox :checked="checked.includes(row.id)" :aria-label="`选择问答 ${row.question}`" @change="toggleRow(row.id)" />
          <div class="queue-message">
            <button class="question-title" type="button" @click="openRow(row)">{{ row.question }}</button>
            <OctoMarkdown :text="row.answer" compact empty="没有送达的 Bot 答复" />
            <p v-if="row.review?.reason" class="hint review-reason">{{ row.review.reason }}</p>
          </div>
          <div class="row queue-status">
            <t-tag size="small" variant="light" :theme="row.outcome === 'answered' ? 'default' : 'warning'">
              {{ outcomes[row.outcome] || row.outcome }}
            </t-tag>
            <span v-if="row.unanswered_repeats > 1">同一问法疑似未答 {{ row.unanswered_repeats }} 次</span>
            <span v-if="row.unavailable_sources">{{ row.unavailable_sources }} 条资料线索当前不可读取</span>
            <t-tag v-if="row.review?.suggestion" size="small" variant="light">{{ suggestions[row.review.suggestion] }}</t-tag>
            <span v-if="row.review?.disposition">{{ dispositions[row.review.disposition] }}</span>
            <span v-if="row.review?.topic">{{ row.review.topic }}</span>
            <span v-if="row.review?.issue_id">已关联问题</span>
          </div>
          <div class="queue-meta hint">
            <time :datetime="row.created_at">{{ new Date(row.created_at).toLocaleString() }}</time>
            <span>{{ row.sources?.length ? `${row.sources.length} 条资料线索` : '未留存资料线索' }}</span>
          </div>
        </article>
      </div>
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

    <t-drawer v-model:visible="drawer" attach="body" header="问答复核" size="min(750px, 100vw)" :footer="false"
      :close-btn="true" :close-on-esc-keydown="true">
      <div v-if="selected" class="review">
        <section class="review-intro">
          <div class="row">
            <t-tag size="small" variant="light">{{ outcomes[selected.outcome] || selected.outcome }}</t-tag>
            <t-tag v-if="selected.review?.suggestion" size="small" variant="light">{{ suggestions[selected.review.suggestion] }}</t-tag>
            <span class="hint">{{ selected.sources?.length ? `${selected.sources.length} 条资料线索` : '本次问答未留存结构化资料线索' }}</span>
          </div>
          <OctoMarkdown :text="selected.question" />
          <p v-if="selected.review?.reason" class="hint">{{ selected.review.reason }}</p>
        </section>
        <label>负责核查的知识库
          <t-select v-model="libraryID" :options="libraryOptions" :disabled="analyzing || confirming"
            placeholder="选择该问题对应的库" />
        </label>
        <div class="row">
          <t-button v-if="selected.review?.state === 'done'" :disabled="!libraryID || analyzing" @click="analyze(true)">查看已复核证据</t-button>
          <t-button :loading="analyzing" :disabled="!libraryID || confirming" @click="analyze(false)">用 AI 复核</t-button>
          <span v-if="!libraryID" class="hint">{{ libraryOptions.length ? '请选择知识库后运行复核。' : '当前范围暂无可用于复核的知识库。' }}</span>
          <span v-else-if="selected.unanswered_repeats > 1" class="hint">同时核对最多 6 条同问法的疑似未答记录。</span>
        </div>
        <details :key="selected.id" class="answer-original">
          <summary>查看 Bot 原答</summary>
          <OctoMarkdown :text="selected.answer" empty="没有送达的 Bot 答复" />
        </details>
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
const readFilters = () => ({
  filter:filter.value, library:filterLibrary.value, keyword:keyword.value,
  topic:topic.value, group:group.value, from:from.value, to:to.value,
})
const applied = ref(readFilters())
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
const frequentGroups = computed(() => (sample.value?.stats.groups ?? []).filter(group => group.count > 1).slice(0, 8))
const day = (s: string) => new Date(s).toLocaleDateString('zh-CN', {timeZone:'Asia/Shanghai'})
const trendMax = computed(() => Math.max(1, ...(sample.value?.stats.days ?? []).map(b => b.count)))
const metrics = computed(() => {
  const stats = sample.value?.stats
  return stats ? [
    {label:'待关注',value:stats.needs_review,filter:'attention'},
    {label:'资料不可用',value:stats.unavailable,filter:'unavailable'},
    {label:'处理或检索异常',value:stats.failed,filter:'failed'},
    {label:'提问量',value:stats.questions,filter:'all'},
    {label:'提问人数',value:stats.users,filter:'all'},
    {label:'AI 复核覆盖',value:`${stats.reviewed} / ${stats.questions}`,filter:'reviewed'},
  ] : []
})
const drillDown = (outcome:string, id = applied.value.group, label = applied.value.topic) => ({
  name:'knowledgeQuality',
  query:{...applied.value, scope:scopeID.value, filter:outcome, topic:label, group:id},
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
  if (!scopeID.value) { loading.value = false; return }
  loading.value = true
  error.value = ''
  try {
    const params = applied.value
    const stop = params.to ? new Date(new Date(`${params.to}T00:00:00+08:00`).getTime() + 86400000).toISOString() : undefined
    const response = await listQuality(scopeID.value,{
      filter:params.filter, keyword:params.keyword, topic:params.topic, group:params.group, library_id:params.library,
      from:params.from ? `${params.from}T00:00:00+08:00` : undefined, to:stop, page:page.value, page_size:pageSize,
    })
    if (version === listVersion) { sample.value = response.data; checked.value = [] }
  } catch {
    if (version === listVersion) error.value = '读取问答失败，请检查区域和访问权限。'
  } finally {
    if (version === listVersion) loading.value = false
  }
}
function applyFilters() {
  if (from.value && to.value && from.value > to.value) {
    error.value = '结束日期须晚于或等于开始日期。'
    return
  }
  applied.value = readFilters()
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
.quality, .review { display: grid; min-width: 0; gap: var(--td-comp-margin-xl); }
.row { display: flex; align-items: center; flex-wrap: wrap; gap: var(--td-comp-margin-m); }
.toolbar { display: flex; align-items: end; flex-wrap: wrap; gap: var(--td-comp-margin-m); }
.toolbar > label { flex: 1 1 10rem; min-width: 0; }
.toolbar > label > :deep(.t-select__wrap), .toolbar > label > :deep(.t-input__wrap) { width: 100%; min-width: 0; }
.toolbar > label:first-child { flex-grow: 1.65; }
.toolbar > label, .review > label, .review section > label, .review section { display: grid; gap: var(--td-comp-margin-s); }
.filters, .extra-filters { display: grid; gap: var(--td-comp-margin-m); }
.extra-filters summary, .answer-original summary, .similar summary { cursor: pointer; font-weight: 500; }
.advanced { margin-top: var(--td-comp-margin-m); }
.turn, .review section, .queue, .answer-original, .metric {
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--td-component-border);
  border-radius: var(--app-radius-md);
}
.queue { padding: 0; }
.queue-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) minmax(7rem, .28fr) minmax(8rem, auto);
  gap: var(--td-comp-margin-m);
  align-items: start;
  padding: 14px 16px;
}
.queue-row + .queue-row { border-top: 1px solid var(--td-component-border); }
.queue-row:hover { background: var(--td-bg-color-container-hover); }
.queue-message, .queue-meta { display: grid; min-width: 0; gap: var(--td-comp-margin-xs); }
.queue-meta, .selection, .queue-status, .extra-filters { font-size: var(--td-font-size-body-medium); }
.selection { margin-bottom: var(--td-comp-margin-m); }
.queue-status, .review-reason { overflow-wrap: anywhere; }
.question-title {
  justify-self: start;
  max-width: 100%;
  border: 0;
  padding: 0;
  background: none;
  color: var(--td-text-color-primary);
  font: inherit;
  font-weight: 600;
  text-align: left;
  cursor: pointer;
  overflow-wrap: anywhere;
}
.question-title:hover, .ranking:hover, .similar-pair a:hover { color: var(--td-brand-color); }
.queue + .t-pagination { margin-top: var(--td-comp-margin-m); }
h3, p, blockquote { margin: 0; }
.turn { display: grid; gap: var(--td-comp-margin-m); }
.turn h3 { font-size: var(--td-font-size-title-medium); }
.hint, small { color: var(--td-text-color-secondary); }
.metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 9rem), 1fr)); gap: var(--td-comp-margin-m); }
.metric { display: grid; gap: var(--td-comp-margin-s); text-decoration: none; color: var(--td-text-color-secondary); }
.metric:hover { border-color: var(--td-brand-color); color: var(--td-brand-color); }
.metric strong { color: var(--td-text-color-primary); font-size: var(--td-font-size-headline-small); font-variant-numeric: tabular-nums; }
.overview-panels { display: grid; grid-template-columns: 1fr 1.35fr .85fr; gap: var(--td-comp-margin-m); align-items: start; }
.overview-panels > section { min-width: 0; }
.ranking, .similar-pair { display: grid; align-items: start; gap: var(--td-comp-margin-m); border-top: 1px solid var(--td-component-border); padding-top: var(--td-comp-margin-m); }
.ranking { grid-template-columns: minmax(0, 1fr) auto; overflow-wrap: anywhere; text-decoration: none; color: var(--td-text-color-primary); }
.ranking small { display: block; }
.similar-pair { grid-template-columns: repeat(2, minmax(0, 1fr)); margin-top: var(--td-comp-margin-m); padding-bottom: var(--td-comp-margin-m); }
.similar-pair a { color: var(--td-text-color-primary); overflow-wrap: anywhere; }
.trend { display:grid; grid-template-columns:90px 1fr auto; gap:8px; align-items:center; }
.trend meter { width:100%; min-width:0; accent-color: var(--td-brand-color); }
.review p, h3 { white-space: pre-wrap; overflow-wrap: anywhere; }
.answer-original > .octo-markdown, .answer-original > .octo-markdown-empty { margin-top: var(--td-comp-margin-m); }
blockquote { border-left: 2px solid var(--td-component-border); padding-left: 12px; }
@media(max-width: 1060px) {
  .overview-panels { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media(max-width: 720px) {
  .overview-panels, .similar-pair { grid-template-columns: minmax(0, 1fr); }
  .toolbar > label { flex-basis: 100%; }
  .queue-row { grid-template-columns: auto minmax(0, 1fr); }
  .queue-status, .queue-meta { grid-column: 2; }
  .queue-meta { display: flex; flex-wrap: wrap; }
}
</style>
