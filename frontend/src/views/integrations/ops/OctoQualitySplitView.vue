<template>
  <div class="q-shell">
    <div class="q-subbar">
      <div class="seg">
        <button type="button" :aria-pressed="sub === 'review'" @click="sub = 'review'">极速审阅流水线 ({{ page?.total ?? 0 }})</button>
        <button type="button" :aria-pressed="sub === 'insight'" @click="sub = 'insight'">问法洞察与归并</button>
      </div>
      <div class="subbar-r">
        <span class="mini">快捷键 J / K 切条 · 1 无需处理 · 2 关联 · 3 一键登记</span>
        <t-button size="small" variant="outline" :loading="retrying" @click="retryAllFailed">重试失败复核</t-button>
      </div>
    </div>

    <t-loading :loading="loading" class="q-main">
      <div v-if="sub === 'review'" class="split-shell">
        <div class="split-list">
          <t-alert v-if="page?.truncated" theme="warning" :close="false" class="trunc-alert">
            当前窗口超过 12,000 条样本上限已截断，请缩短日期核对完整记录。
          </t-alert>
          <div class="list-hd">
            <div class="pills">
              <button
                v-for="f in filterTabs"
                :key="f.v"
                type="button"
                class="pill-btn"
                :aria-pressed="qFilter === f.v"
                @click="pickFilter(f.v)"
              >
                {{ f.label }}
              </button>
            </div>
            <div class="list-meta">
              <t-checkbox :checked="allChecked" @change="toggleAll">全选当前列</t-checkbox>
              <span class="mini">共 {{ rows.length }} 条</span>
            </div>
          </div>
          <div class="list-body">
            <div
              v-for="row in rows"
              :key="row.id"
              class="q-card"
              :class="{ active: active?.id === row.id }"
              @click="selectRow(row)"
            >
              <t-checkbox :checked="checkedIds.has(row.id)" @click.stop @change="(v: boolean) => toggleOne(row.id, v)" />
              <div class="min0">
                <div class="q-card-top">
                  <span class="q-card-title">{{ row.question }}</span>
                  <t-tag size="small" variant="light" :theme="rowTone(row)">{{ rowBadge(row) }}</t-tag>
                </div>
                <div class="q-card-ans">{{ row.answer || '（无完整答复 / 对话中断）' }}</div>
                <div class="q-card-meta">
                  <t-tag size="small" variant="outline">{{ sourceLabel(row.source_type) }}</t-tag>
                  <span>{{ row.scope_name || '已核验区域' }}</span>
                  <span v-if="row.unanswered_repeats > 1">· 同问法 {{ row.unanswered_repeats }} 次</span>
                  <t-tag v-if="row.review?.issue_id" size="small" theme="primary" variant="light">已转问题</t-tag>
                </div>
              </div>
            </div>
            <t-empty v-if="!rows.length && !loading" description="当前筛选下没有问答样本" style="padding: 40px 0" />
          </div>
        </div>

        <div class="split-detail">
          <template v-if="active">
            <div class="sd-hd">
              <div class="min0">
                <div class="sd-title-row">
                  <h3>{{ active.question }}</h3>
                  <t-tag size="small" variant="light">{{ sourceLabel(active.source_type) }}</t-tag>
                </div>
                <div class="mini">
                  {{ active.asker_name || '成员' }} · {{ formatTime(active.created_at) }} · {{ active.scope_name }} ·
                  <span class="msg-ref" :title="active.id" @click="copyMsgId(active.id)">{{ shortMsgId(active.id) }}</span>
                </div>
              </div>
              <div class="sd-nav">
                <span class="mini">{{ activeIndex + 1 }} / {{ rows.length }}</span>
                <t-button size="small" variant="outline" :disabled="activeIndex <= 0" @click="step(-1)">上一条 K</t-button>
                <t-button size="small" variant="outline" :disabled="activeIndex >= rows.length - 1" @click="step(1)">下一条 J</t-button>
              </div>
            </div>

            <div class="sd-body">
              <t-alert v-if="active.source_type === 'dm'" theme="info" :close="false">
                <strong>私聊样本隔离边界</strong>：仅工作区管理员可见，不与群聊跨范围混并问题组；如需回告仅发回原私聊会话。
              </t-alert>

              <div class="bubble user">
                <div class="bubble-hd"><b>{{ active.asker_name || '用户' }}（原始提问）</b><span>{{ active.scope_name }}</span></div>
                <OctoMarkdown :text="active.question" />
              </div>

              <div class="bubble">
                <div class="bubble-hd">
                  <b>{{ active.bot_name || 'Octo Bot' }}（当时答复）</b>
                  <t-tag size="small" :theme="active.interrupted ? 'danger' : 'success'" variant="light">
                    {{ active.interrupted ? 'is_completed=false 中途中断' : '已完成' }}
                  </t-tag>
                </div>
                <OctoMarkdown :text="active.answer" empty="（未生成完整答复）" />
                <details v-if="active.agent_trace?.length" class="trace-det">
                  <summary>查看 Agent 检索轨迹（{{ active.agent_trace.length }} 步）</summary>
                  <ul>
                    <li v-for="(tr, idx) in active.agent_trace" :key="idx"><code>{{ tr }}</code></li>
                  </ul>
                </details>
                <div v-if="active.sources?.length" class="src-list">
                  <span class="mini">命中资产线索（{{ active.sources.length }} 条 · 不可用 {{ active.unavailable_sources }} 条）：</span>
                  <div class="src-tags">
                    <t-tag v-for="(s, i) in active.sources" :key="i" size="small" variant="outline">
                      {{ s.wiki_slug || s.chunk_id || s.library }}
                    </t-tag>
                  </div>
                </div>
              </div>

              <div v-if="active.review?.state === 'failed'" class="ai-card err">
                <div class="row-between">
                  <b>AI 后台复核失败</b>
                  <t-button size="small" theme="primary" :loading="retrying" @click="retrySingle(active.id)">立即重跑 AI 复核</t-button>
                </div>
                <div class="mini">将重新入队使用当前后台复核模型比对现行知识库文档与 Wiki。</div>
              </div>
              <div v-else-if="active.review?.reason" class="ai-card">
                <div class="row-between">
                  <b>AI 复核证据与初判</b>
                  <t-tag size="small" theme="primary" variant="light">{{ active.review.suggestion || '已复核' }}</t-tag>
                </div>
                <p class="ai-reason">{{ active.review.reason }}</p>
              </div>
            </div>

            <div class="sd-dock">
              <div v-if="dockMode === 'new'" class="dock-panel">
                <div class="row-between">
                  <b>一键登记为问题（建单后直达工单工作台）</b>
                  <t-button size="small" variant="text" @click="dockMode = ''">收起</t-button>
                </div>
                <div class="form-grid">
                  <t-input v-model="newIssue.title" placeholder="问题标题" class="full" />
                  <t-select v-model="newIssue.kbId" :options="kbSelectOpts" placeholder="选择所属产品库" filterable />
                  <t-select v-model="newIssue.kind" :options="kindOpts" />
                  <t-input v-model="newIssue.note" placeholder="核查结论（可选）" class="full" />
                </div>
                <div class="row-between">
                  <span class="mini">跟进人可选填，不强制先配置知识库联系人</span>
                  <t-button theme="primary" size="small" :loading="submitting" @click="submitNewIssue">确认登记并进入处理 →</t-button>
                </div>
              </div>

              <div class="dock-actions">
                <div class="dock-l">
                  <t-button size="small" theme="success" variant="outline" :loading="submitting" @click="markDismiss">
                    ✓ 无需处理 (1)
                  </t-button>
                  <t-button size="small" variant="outline" @click="dockMode = dockMode === 'new' ? '' : 'new'">
                    + 一键登记为问题 (3)
                  </t-button>
                </div>
                <t-button v-if="active.review?.issue_id" size="small" theme="primary" @click="router.push({ name: 'knowledgeIssueDetail', params: { issueId: active.review.issue_id } })">
                  打开已关联工单 →
                </t-button>
              </div>
            </div>
          </template>
          <t-empty v-else description="请在左侧选择一条问答样本" style="margin: auto" />
        </div>
      </div>

      <div v-else class="insight-wrap">
        <section class="panel-card">
          <div class="panel-hd"><h3>高频问题排行</h3><small>按同问法聚合</small></div>
          <t-table :data="page?.stats.groups || []" :columns="groupCols" row-key="id" size="small" />
        </section>
      </div>
    </t-loading>

    <div v-if="checkedIds.size > 0 && sub === 'review'" class="float-bar">
      <b>已选 {{ checkedIds.size }} 条问答</b>
      <t-button size="small" variant="outline" @click="bulkDismiss">批量无需处理</t-button>
      <t-button size="small" variant="text" @click="checkedIds.clear()">取消选择</t-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  batchQuality,
  convertQualityToIssue,
  listQuality,
  retryFailedQuality,
  type IssueKind,
  type QualityPage,
  type QualityRow,
} from '@/api/octo-business'
import OctoMarkdown from '../OctoMarkdown.vue'
import { opsScope, resolveScopeQuery } from './useOctoOpsScope'

const props = defineProps<{ kbs: Array<{ id: string; name: string }> }>()
const route = useRoute()
const router = useRouter()

const sub = ref<'review' | 'insight'>('review')
const qFilter = ref(String(route.query.filter || 'attention'))
const loading = ref(false)
const retrying = ref(false)
const submitting = ref(false)
const page = ref<QualityPage | null>(null)
const activeId = ref(String(route.query.sample || ''))
const checkedIds = reactive(new Set<string>())
const dockMode = ref<'' | 'new'>('')
const newIssue = reactive<{ title: string; kbId: string; kind: IssueKind; note: string }>({
  title: '',
  kbId: '',
  kind: 'missing',
  note: '',
})

const filterTabs = [
  { v: 'attention', label: '待审' },
  { v: 'process', label: 'AI 建议处理' },
  { v: 'abnormal', label: '中断/失败' },
  { v: 'dm', label: '仅私聊' },
  { v: 'done', label: '已处理' },
  { v: 'all', label: '全部' },
]

const kindOpts = [
  { value: 'missing', label: '知识缺口' },
  { value: 'bug', label: 'Bug 反馈' },
  { value: 'suggestion', label: '优化建议' },
]

const groupCols = [
  { colKey: 'question', title: '代表问法' },
  { colKey: 'count', title: '提问次数', width: 100 },
  { colKey: 'users', title: '提问人数', width: 100 },
  { colKey: 'gaps', title: '疑似缺口', width: 100 },
]

const rows = computed(() => page.value?.rows || [])
const active = computed(() => rows.value.find((r) => r.id === activeId.value) || rows.value[0] || null)
const activeIndex = computed(() => (active.value ? rows.value.findIndex((r) => r.id === active.value!.id) : -1))
const allChecked = computed(() => rows.value.length > 0 && rows.value.every((r) => checkedIds.has(r.id)))
const kbSelectOpts = computed(() =>
  props.kbs.filter((k) => !k.name.startsWith('__')).map((k) => ({ label: k.name, value: k.id })),
)

function sourceLabel(t?: string) {
  if (t === 'dm') return '私聊'
  if (t === 'subarea') return '子区'
  return '主群'
}
function rowBadge(r: QualityRow) {
  if (r.interrupted) return '对话中断'
  if (r.review?.state === 'failed') return 'AI 复核失败'
  if (r.review?.suggestion === 'process') return '建议处理'
  if (r.outcome === 'suspected_gap') return '疑似缺口'
  return '待核查'
}
function rowTone(r: QualityRow): 'danger' | 'warning' | 'success' | 'default' {
  if (r.interrupted || r.review?.state === 'failed') return 'danger'
  if (r.review?.suggestion === 'process' || r.outcome === 'suspected_gap') return 'warning'
  if (r.review?.suggestion === 'dismiss') return 'success'
  return 'default'
}
function formatTime(v: string) {
  return v ? new Date(v).toLocaleString() : ''
}
function shortMsgId(id: string) {
  if (!id) return ''
  const parts = id.split(':')
  const tail = parts[parts.length - 1] || id
  return `消息 #${tail.length > 10 ? tail.slice(-8) : tail}`
}
function copyMsgId(id: string) {
  if (!id) return
  navigator.clipboard?.writeText(id).then(() => MessagePlugin.success('已复制完整消息编号')).catch(() => {})
}

function selectRow(row: QualityRow) {
  activeId.value = row.id
  dockMode.value = ''
  newIssue.title = row.question.slice(0, 80)
  newIssue.kbId = row.review?.library_id || kbSelectOpts.value[0]?.value || ''
  newIssue.note = row.review?.reason || ''
}

function step(delta: number) {
  const next = rows.value[activeIndex.value + delta]
  if (next) selectRow(next)
}
function toggleOne(id: string, checked: boolean) {
  if (checked) checkedIds.add(id)
  else checkedIds.delete(id)
}
function toggleAll(checked: boolean) {
  checkedIds.clear()
  if (checked) rows.value.forEach((r) => checkedIds.add(r.id))
}
function pickFilter(v: string) {
  qFilter.value = v
  void load()
}

async function load() {
  loading.value = true
  try {
    const q = resolveScopeQuery(opsScope)
    const isDmOnly = qFilter.value === 'dm'
    const res = await listQuality(q.scope_id, {
      library_id: q.knowledge_base_id || undefined,
      keyword: q.keyword || undefined,
      filter: isDmOnly ? 'all' : qFilter.value,
      source_type: isDmOnly ? 'dm' : q.source_type || undefined,
      channel_id: q.channel_id || undefined,
      include_dm: true,
      from: q.from,
      to: q.to,
      page: 1,
      page_size: 50,
    })
    page.value = res.data
    if (res.data.rows.length && !res.data.rows.some((r) => r.id === activeId.value)) {
      selectRow(res.data.rows[0]!)
    }
  } catch {
    MessagePlugin.error('加载问答质量样本失败')
  } finally {
    loading.value = false
  }
}

async function retryAllFailed() {
  retrying.value = true
  try {
    const res = await retryFailedQuality({})
    MessagePlugin.success(`已重跑 ${res.data.retried} 条失败的 AI 复核`)
    await load()
  } catch {
    MessagePlugin.error('重跑失败复核出错')
  } finally {
    retrying.value = false
  }
}

async function retrySingle(messageId: string) {
  retrying.value = true
  try {
    await retryFailedQuality({ message_id: messageId })
    MessagePlugin.success('已重新入队 AI 复核')
    await load()
  } finally {
    retrying.value = false
  }
}

async function markDismiss() {
  if (!active.value?.scope_id) {
    MessagePlugin.info('私聊样本请使用「一键登记为问题」或核对后切至下一条')
    return
  }
  submitting.value = true
  try {
    await batchQuality({ scope_id: active.value.scope_id, message_ids: [active.value.id], action: 'disposition', value: 'dismiss' })
    MessagePlugin.success('已确认无需处理')
    await load()
  } finally {
    submitting.value = false
  }
}

async function bulkDismiss() {
  const firstScope = rows.value.find((r) => checkedIds.has(r.id) && r.scope_id)?.scope_id
  if (!firstScope) return
  await batchQuality({ scope_id: firstScope, message_ids: Array.from(checkedIds), action: 'disposition', value: 'dismiss' })
  checkedIds.clear()
  MessagePlugin.success('已批量标记无需处理')
  await load()
}

async function submitNewIssue() {
  if (!active.value || !newIssue.title.trim() || !newIssue.kbId) {
    MessagePlugin.warning('请填写问题标题并选择所属产品库')
    return
  }
  submitting.value = true
  try {
    const res = await convertQualityToIssue(active.value.id, {
      scope_id: active.value.scope_id,
      channel_id: active.value.channel_id,
      is_direct: active.value.source_type === 'dm',
      title: newIssue.title.trim(),
      knowledge_base_id: newIssue.kbId,
      kind: newIssue.kind,
      note: newIssue.note.trim(),
    })
    MessagePlugin.success('已登记为新问题，直达工单工作台')
    router.push({ name: 'knowledgeIssueDetail', params: { issueId: res.data.id } })
  } catch {
    MessagePlugin.error('登记问题失败')
  } finally {
    submitting.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  const tag = (document.activeElement?.tagName || '').toUpperCase()
  if (['INPUT', 'TEXTAREA', 'SELECT'].includes(tag) || sub.value !== 'review') return
  if (e.key === 'j' || e.key === 'J') step(1)
  else if (e.key === 'k' || e.key === 'K') step(-1)
  else if (e.key === '1') void markDismiss()
  else if (e.key === '3') dockMode.value = 'new'
  else if (e.key === 'Escape') dockMode.value = ''
}

defineExpose({ reload: load })
onMounted(() => {
  void load()
  window.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<style scoped lang="less">
.q-shell { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.q-subbar {
  display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap;
  padding: 7px 20px; background: var(--td-bg-color-secondarycontainer); border-bottom: 1px solid var(--td-component-border);
}
.seg { display: inline-flex; padding: 2px; border-radius: 6px; background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); }
.seg button { height: 26px; padding: 0 11px; border: 0; border-radius: 4px; background: transparent; color: var(--td-text-color-secondary); font-size: 12.5px; cursor: pointer; }
.seg button[aria-pressed='true'] { background: var(--td-brand-color-light); color: var(--td-brand-color); font-weight: 600; }
.subbar-r { display: flex; align-items: center; gap: 10px; }
.q-main { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.split-shell { flex: 1; min-height: 540px; display: grid; grid-template-columns: minmax(320px, 36%) minmax(0, 64%); overflow: hidden; }
.split-list { display: flex; flex-direction: column; border-right: 1px solid var(--td-component-border); background: var(--td-bg-color-container); min-height: 0; }
.list-hd { padding: 10px 14px; border-bottom: 1px solid var(--td-component-border); display: grid; gap: 8px; }
.pills { display: flex; gap: 4px; flex-wrap: wrap; }
.pill-btn { height: 26px; padding: 0 9px; border: 1px solid transparent; border-radius: 99px; background: transparent; font-size: 12px; color: var(--td-text-color-secondary); cursor: pointer; }
.pill-btn[aria-pressed='true'] { background: var(--td-brand-color-light); color: var(--td-brand-color); font-weight: 600; }
.list-meta { display: flex; justify-content: space-between; align-items: center; }
.list-body { flex: 1; overflow-y: auto; }
.q-card { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 8px; padding: 11px 14px; border-bottom: 1px solid var(--td-component-border); cursor: pointer; }
.q-card:hover { background: var(--td-bg-color-container-hover); }
.q-card.active { background: var(--td-brand-color-light); }
.q-card-top { display: flex; justify-content: space-between; align-items: center; gap: 6px; }
.q-card-title { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.q-card-ans { font-size: 12px; color: var(--td-text-color-secondary); margin-top: 3px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.q-card-meta { margin-top: 6px; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 11.5px; color: var(--td-text-color-placeholder); }
.split-detail { display: flex; flex-direction: column; min-height: 0; background: var(--td-bg-color-page); }
.sd-hd { display: flex; justify-content: space-between; align-items: center; gap: 12px; padding: 12px 20px; background: var(--td-bg-color-container); border-bottom: 1px solid var(--td-component-border); }
.sd-title-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.sd-title-row h3 { margin: 0; font-size: 15px; }
.sd-nav { display: flex; align-items: center; gap: 6px; }
.sd-body { flex: 1; overflow-y: auto; padding: 14px 20px; display: grid; gap: 12px; align-content: start; }
.bubble { padding: 12px 14px; border-radius: 10px; background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); }
.bubble.user { background: var(--td-brand-color-light); border-color: var(--td-brand-color-3); }
.bubble-hd { display: flex; justify-content: space-between; font-size: 12px; color: var(--td-text-color-placeholder); margin-bottom: 6px; }
.trace-det { margin-top: 8px; padding-top: 8px; border-top: 1px dashed var(--td-component-border); font-size: 12px; }
.src-list { margin-top: 8px; padding-top: 8px; border-top: 1px dashed var(--td-component-border); display: grid; gap: 6px; }
.src-tags { display: flex; flex-wrap: wrap; gap: 6px; }
.ai-card { padding: 12px 14px; border-radius: 10px; background: var(--td-bg-color-container); border: 1px solid var(--td-brand-color-3); border-left: 3px solid var(--td-brand-color); display: grid; gap: 6px; }
.ai-card.err { border-left-color: var(--td-error-color); border-color: var(--td-error-color-3); }
.ai-reason { margin: 0; font-size: 13px; }
.sd-dock { padding: 11px 20px; background: var(--td-bg-color-container); border-top: 1px solid var(--td-component-border); display: grid; gap: 10px; }
.dock-panel { padding: 12px; border-radius: 8px; border: 1px solid var(--td-brand-color-3); background: var(--td-bg-color-secondarycontainer); display: grid; gap: 10px; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.form-grid .full { grid-column: 1 / -1; }
.dock-actions, .dock-l, .row-between { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; }
.float-bar { position: fixed; bottom: 20px; left: 50%; transform: translateX(-50%); display: flex; align-items: center; gap: 10px; padding: 9px 16px; border-radius: 10px; background: var(--td-text-color-primary); color: var(--td-bg-color-container); z-index: 40; box-shadow: 0 12px 32px rgba(0,0,0,0.22); }
.insight-wrap { padding: 18px 22px; max-width: 1280px; margin: 0 auto; width: 100%; }
.panel-card { background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px; overflow: hidden; }
.panel-hd { display: flex; justify-content: space-between; align-items: center; padding: 12px 16px; border-bottom: 1px solid var(--td-component-border); }
.panel-hd h3 { margin: 0; font-size: 14px; }
.min0 { min-width: 0; }
.mini { font-size: 12px; color: var(--td-text-color-placeholder); }
.msg-ref { cursor: pointer; text-decoration: underline dotted; }
.msg-ref:hover { color: var(--td-brand-color); }
@media (max-width: 960px) { .split-shell { grid-template-columns: minmax(0, 1fr); } }
</style>
