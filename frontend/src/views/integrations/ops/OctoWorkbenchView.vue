<template>
  <t-loading :loading="loading" class="wb-root">
    <t-alert v-if="error" theme="error" class="mb12">
      {{ error }}
      <t-button variant="text" size="small" @click="load">重试</t-button>
    </t-alert>

    <div v-if="summary" class="wb-wrap">
      <div v-if="hasAlerts" class="alert-stack">
        <div v-if="summary.alerts.interrupted_count || summary.alerts.failed_ai_reviews" class="alert-bar err">
          <div class="alert-l">
            <t-tag theme="danger" size="small" variant="dark">质量异常</t-tag>
            <span>
              <strong>{{ summary.alerts.interrupted_count }} 轮对话中断 · {{ summary.alerts.failed_ai_reviews }} 条 AI 复核失败</strong>
            </span>
          </div>
          <t-button theme="primary" size="small" @click="goQuality('abnormal')">直达处理</t-button>
        </div>
        <div v-if="summary.alerts.partial_sync_kbs.length" class="alert-bar warn">
          <div class="alert-l">
            <t-tag theme="warning" size="small" variant="dark">同步告警</t-tag>
            <span>
              <strong>{{ summary.alerts.partial_sync_kbs.map((k) => k.name).join('、') }}</strong>
              近 14 天存在本地目录部分同步失败或残留 disabled 文档
            </span>
          </div>
          <t-button size="small" variant="outline" @click="openSyncDiag('')">查看同步诊断</t-button>
        </div>
      </div>

      <div class="kpi-grid">
        <button type="button" class="kpi" @click="goQuality('process')">
          <div class="kpi-top"><span>AI 建议处理问答</span><span class="dot warn" /></div>
          <div class="kpi-mid">
            <div class="kpi-val">{{ summary.kpis.ai_process_count }}<small>条</small></div>
            <t-tag size="small" theme="warning" variant="light">已转化 {{ summary.kpis.ai_converted_count }}</t-tag>
          </div>
          <div class="kpi-foot">资料缺口、旧版资料与中断 · 一键转单</div>
        </button>
        <button type="button" class="kpi" @click="router.push({ name: 'knowledgeIssues' })">
          <div class="kpi-top"><span>开放问题</span><span class="dot brand" /></div>
          <div class="kpi-mid">
            <div class="kpi-val">{{ summary.kpis.open_issues }}<small>件</small></div>
            <t-tag size="small" theme="success" variant="light">已回告 {{ summary.kpis.replied_issues }}</t-tag>
          </div>
          <div class="kpi-foot">默认轻量化一键复测与原渠道回告</div>
        </button>
        <button type="button" class="kpi" @click="goQuality('abnormal')">
          <div class="kpi-top"><span>对话中断 / 复核失败</span><span class="dot err" /></div>
          <div class="kpi-mid">
            <div class="kpi-val">{{ summary.kpis.interrupted_count + summary.kpis.failed_ai_reviews }}<small>条</small></div>
            <t-tag size="small" theme="danger" variant="light">{{ summary.kpis.interrupted_rate.toFixed(1) }}% 中断率</t-tag>
          </div>
          <div class="kpi-foot">含 is_completed=false 超时与复核异常</div>
        </button>
        <button type="button" class="kpi" @click="router.push({ name: 'knowledgeReports' })">
          <div class="kpi-top"><span>产品知识资产健康</span><span class="dot ok" /></div>
          <div class="kpi-mid">
            <div class="kpi-val">{{ summary.kpis.kb_count }}<small>知识库</small></div>
            <t-tag size="small" theme="success" variant="light">{{ summary.kpis.wiki_pages }} Wiki</t-tag>
          </div>
          <div class="kpi-foot">PPT {{ summary.kpis.pptx_count }} · 表格 {{ summary.kpis.xlsx_count }}</div>
        </button>
      </div>

      <div class="wb-grid">
        <section class="panel-card">
          <div class="panel-hd">
            <div>
              <h3>今日优先行动流</h3>
              <small>打通「AI 建议处理问答 → 问题闭环」，行内直达</small>
            </div>
            <div class="pills">
              <button
                v-for="tab in streamTabs"
                :key="tab.v"
                type="button"
                class="pill-btn"
                :aria-pressed="streamFilter === tab.v"
                @click="streamFilter = tab.v"
              >
                {{ tab.label }}
              </button>
            </div>
          </div>
          <div v-if="filteredStream.length" class="stream-list">
            <div v-for="item in filteredStream" :key="`${item.kind}-${item.id}`" class="stream-row">
              <span class="dot" :class="item.badge_tone" />
              <div class="min0">
                <div class="stream-title">
                  <span class="ellipsis">{{ item.title }}</span>
                  <t-tag size="small" variant="light" :theme="tagTheme(item.badge_tone)">{{ item.badge }}</t-tag>
                </div>
                <div class="stream-sub">{{ item.sub_text }}</div>
              </div>
              <div class="stream-meta">
                <b>{{ item.meta_top }}</b>
                <span>{{ item.meta_sub }}</span>
              </div>
              <t-button
                size="small"
                :theme="item.primary ? 'primary' : 'default'"
                :variant="item.primary ? 'base' : 'outline'"
                :loading="convertingId === item.id"
                @click="handleAction(item)"
              >
                {{ item.cta }}
              </t-button>
            </div>
          </div>
          <t-empty v-else description="当前筛选范围内暂无待处理行动项" style="padding: 36px 0" />
        </section>

        <div class="wb-side">
          <section class="panel-card">
            <div class="panel-hd">
              <h3>产品知识库健康矩阵</h3>
              <small>本地目录同步 · PPT/Wiki</small>
            </div>
            <div class="panel-bd">
              <div v-for="kb in summary.kb_health" :key="kb.id" class="kb-row">
                <div class="min0">
                  <div class="kb-name">{{ kb.name }}</div>
                  <div class="kb-sub">
                    {{ kb.docs }} 篇（PPT {{ kb.pptx }} / 表 {{ kb.xlsx }}）· {{ kb.wiki_pages }} Wiki
                    <span v-if="kb.last_sync_at"> · 同步 {{ kb.last_sync_at }}</span>
                  </div>
                </div>
                <t-tag
                  v-if="kb.sync_status === 'partial' || kb.sync_status === 'failed'"
                  theme="warning"
                  variant="light"
                  size="small"
                  style="cursor: pointer"
                  @click="openSyncDiag(kb.id)"
                >
                  部分失败 ({{ kb.partial_runs_14d }})
                </t-tag>
                <t-tag v-else-if="kb.wiki_pages <= 1 && kb.docs > 0" theme="primary" variant="light" size="small">Wiki 待补</t-tag>
                <t-tag v-else theme="success" variant="light" size="small">健康</t-tag>
              </div>
              <t-empty v-if="!summary.kb_health.length" description="暂无知识库统计" />
            </div>
          </section>

          <section class="panel-card">
            <div class="panel-hd">
              <h3>高频业务问法 Top 5</h3>
              <t-button variant="text" size="small" @click="goQuality('all')">全部问答 →</t-button>
            </div>
            <div class="panel-bd">
              <button
                v-for="(hq, idx) in summary.hot_questions"
                :key="hq.id"
                type="button"
                class="hot-row"
                @click="router.push({ name: 'knowledgeQuality', query: { sample: hq.id } })"
              >
                <div class="hot-l">
                  <span class="rank" :class="{ top: idx < 3 }">{{ idx + 1 }}</span>
                  <div class="min0">
                    <span class="hot-q">{{ hq.question }}</span>
                    <span class="mini">{{ hq.knowledge_base_name || '多库' }} · {{ hq.source_type }}</span>
                  </div>
                </div>
                <div class="hot-r">
                  <b>{{ hq.count }} 次</b>
                  <span class="mini">{{ hq.users }} 人</span>
                </div>
              </button>
              <t-empty v-if="!summary.hot_questions.length" description="当前窗口暂无高频问法" />
            </div>
          </section>
        </div>
      </div>
    </div>

    <t-dialog v-model:visible="syncDialogVisible" header="本地目录同步与资产健康诊断" :footer="false" width="600px">
      <div class="diag-list">
        <t-alert
          v-for="kb in diagKBs"
          :key="kb.id"
          :theme="kb.sync_status === 'ok' ? 'success' : 'warning'"
          :close="false"
        >
          <strong>{{ kb.name }}</strong>（{{ kb.schedule || '定时同步' }} · 最近 {{ kb.last_sync_at || '未知' }}）
          <div class="mini">{{ kb.sync_note }} · 近 14 天部分/失败 {{ kb.partial_runs_14d }} 次 · disabled 文档 {{ kb.disabled }} 篇</div>
        </t-alert>
      </div>
    </t-dialog>
  </t-loading>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  convertQualityToIssue,
  workbenchSummary,
  type WorkbenchActionItem,
  type WorkbenchSummary,
} from '@/api/octo-business'
import { opsScope, resolveScopeQuery } from './useOctoOpsScope'

const emit = defineEmits<{ 'counts-updated': [{ pendingQA: number; openIssues: number }] }>()
const router = useRouter()

const loading = ref(false)
const error = ref('')
const summary = ref<WorkbenchSummary | null>(null)
const streamFilter = ref<'all' | 'qa' | 'issue'>('all')
const convertingId = ref('')
const syncDialogVisible = ref(false)
const syncFilterKB = ref('')

const hasAlerts = computed(() => {
  const a = summary.value?.alerts
  return Boolean(a && (a.interrupted_count || a.failed_ai_reviews || a.partial_sync_kbs.length))
})

const streamTabs = computed(() => {
  const all = summary.value?.action_stream || []
  const qa = all.filter((x) => x.kind === 'qa').length
  const iss = all.filter((x) => x.kind === 'issue').length
  return [
    { v: 'all' as const, label: `全部 (${all.length})` },
    { v: 'qa' as const, label: `AI 建议转单 (${qa})` },
    { v: 'issue' as const, label: `开放问题 (${iss})` },
  ]
})

const filteredStream = computed(() => {
  const all = summary.value?.action_stream || []
  if (streamFilter.value === 'all') return all
  return all.filter((x) => x.kind === streamFilter.value)
})

const diagKBs = computed(() => {
  const list = summary.value?.kb_health || []
  return syncFilterKB.value ? list.filter((k) => k.id === syncFilterKB.value) : list
})

function tagTheme(tone: string): 'danger' | 'warning' | 'primary' | 'success' {
  if (tone === 'err') return 'danger'
  if (tone === 'warn') return 'warning'
  if (tone === 'ok') return 'success'
  return 'primary'
}

function goQuality(filter: string) {
  router.push({ name: 'knowledgeQuality', query: { filter } })
}

function openSyncDiag(kbId: string) {
  syncFilterKB.value = kbId
  syncDialogVisible.value = true
}

async function handleAction(item: WorkbenchActionItem) {
  if (item.kind === 'issue') {
    router.push({ name: 'knowledgeIssueDetail', params: { issueId: item.id } })
    return
  }
  if (!item.knowledge_base_id) {
    router.push({ name: 'knowledgeQuality', query: { sample: item.id } })
    return
  }
  convertingId.value = item.id
  try {
    const res = await convertQualityToIssue(item.id, {
      scope_id: item.scope_id,
      channel_id: item.channel_id,
      is_direct: item.source_type === '私聊',
      title: item.title,
      knowledge_base_id: item.knowledge_base_id,
      kind: 'missing',
      note: '由工作台今日优先行动流一键转入问题闭环',
    })
    MessagePlugin.success('已一键登记为问题，正在进入工单工作台')
    router.push({ name: 'knowledgeIssueDetail', params: { issueId: res.data.id } })
  } catch {
    MessagePlugin.error('一键转单失败，请在问答质量页核对知识库后登记')
  } finally {
    convertingId.value = ''
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await workbenchSummary(resolveScopeQuery(opsScope))
    summary.value = res.data
    emit('counts-updated', { pendingQA: res.data.pending_qa, openIssues: res.data.open_issues })
  } catch {
    error.value = '加载工作台聚合数据失败，请重试。'
  } finally {
    loading.value = false
  }
}

defineExpose({ reload: load })
onMounted(load)
</script>

<style scoped lang="less">
.wb-root { min-height: 240px; }
.mb12 { margin-bottom: 12px; }
.wb-wrap { max-width: 1440px; margin: 0 auto; display: grid; gap: 15px; }
.wb-wrap > * { min-width: 0; }
.alert-stack { display: grid; gap: 8px; }
.alert-bar {
  display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap;
  padding: 9px 14px; border-radius: 10px; border: 1px solid var(--td-component-border);
  background: var(--td-bg-color-container); font-size: 13px;
}
.alert-bar.err { background: var(--td-error-color-1); border-color: var(--td-error-color-3); }
.alert-bar.warn { background: var(--td-warning-color-1); border-color: var(--td-warning-color-3); }
.alert-l { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; }
.kpi-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.kpi {
  display: flex; flex-direction: column; gap: 6px; padding: 14px 16px; border-radius: 10px;
  background: var(--td-bg-color-container); border: 1px solid var(--td-component-border);
  text-align: left; cursor: pointer; transition: border-color 0.15s;
}
.kpi:hover { border-color: var(--td-brand-color); }
.kpi-top { display: flex; justify-content: space-between; align-items: center; font-size: 12.5px; color: var(--td-text-color-secondary); }
.kpi-mid { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.kpi-val { font-size: 26px; font-weight: 700; color: var(--td-text-color-primary); font-variant-numeric: tabular-nums; }
.kpi-val small { font-size: 12.5px; font-weight: 500; color: var(--td-text-color-placeholder); margin-left: 3px; }
.kpi-foot { font-size: 12px; color: var(--td-text-color-placeholder); }
.dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; flex: 0 0 auto; background: var(--td-text-color-placeholder); }
.dot.err { background: var(--td-error-color); }
.dot.warn { background: var(--td-warning-color); }
.dot.ok { background: var(--td-success-color); }
.dot.brand { background: var(--td-brand-color); }
.wb-grid { display: grid; grid-template-columns: minmax(0, 1.52fr) minmax(0, 0.98fr); gap: 15px; align-items: start; }
.wb-grid > *, .wb-side > * { min-width: 0; }
.wb-side { display: grid; gap: 15px; min-width: 0; }
.panel-card { background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px; overflow: hidden; }
.panel-hd {
  display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap;
  padding: 12px 16px; border-bottom: 1px solid var(--td-component-border);
}
.panel-hd h3 { margin: 0; font-size: 14px; font-weight: 600; display: inline; }
.panel-hd small { font-size: 12px; color: var(--td-text-color-placeholder); margin-left: 8px; }
.panel-bd { padding: 12px 16px; }
.pills { display: flex; gap: 4px; flex-wrap: wrap; }
.pill-btn {
  height: 26px; padding: 0 10px; border: 1px solid transparent; border-radius: 99px;
  background: transparent; color: var(--td-text-color-secondary); font-size: 12px; cursor: pointer;
}
.pill-btn[aria-pressed='true'] { background: var(--td-brand-color-light); border-color: var(--td-brand-color-3); color: var(--td-brand-color); font-weight: 600; }
.stream-row {
  display: grid; grid-template-columns: 10px minmax(0, 1fr) auto auto; align-items: center; gap: 12px;
  padding: 11px 16px; border-top: 1px solid var(--td-component-border);
}
.stream-row:first-child { border-top: 0; }
.stream-title { display: flex; align-items: center; gap: 6px; font-size: 13.5px; font-weight: 600; }
.stream-sub { margin-top: 2px; font-size: 12px; color: var(--td-text-color-placeholder); }
.stream-meta { text-align: right; font-size: 12px; color: var(--td-text-color-placeholder); white-space: nowrap; }
.stream-meta b { display: block; color: var(--td-text-color-secondary); }
.kb-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 9px 0; border-top: 1px solid var(--td-component-border); }
.kb-row:first-child { border-top: 0; padding-top: 0; }
.kb-name { font-size: 13px; font-weight: 600; }
.kb-sub { font-size: 11.5px; color: var(--td-text-color-placeholder); margin-top: 2px; }
.hot-row {
  display: flex; align-items: center; justify-content: space-between; gap: 10px; width: 100%; min-width: 0;
  padding: 8px 0; border: 0; border-top: 1px solid var(--td-component-border); background: transparent; text-align: left; cursor: pointer;
}
.hot-row:first-child { border-top: 0; padding-top: 0; }
.hot-l { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1; }
.hot-r { text-align: right; white-space: nowrap; font-size: 12.5px; }
.rank { width: 18px; height: 18px; border-radius: 4px; background: var(--td-bg-color-secondarycontainer); font-size: 11px; font-weight: 600; display: inline-flex; align-items: center; justify-content: center; flex: 0 0 auto; }
.rank.top { background: var(--td-brand-color-light); color: var(--td-brand-color); }
.hot-q { display: block; font-size: 13px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.min0 { min-width: 0; }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mini { display: block; font-size: 11.5px; color: var(--td-text-color-placeholder); }
.diag-list { display: grid; gap: 8px; }
@media (max-width: 1120px) {
  .wb-grid { grid-template-columns: minmax(0, 1fr); }
  .kpi-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
