<template>
  <div class="rep-root">
    <div class="rep-subbar">
      <div class="seg">
        <button type="button" :aria-pressed="sub === 'overview'" @click="sub = 'overview'">本期报告</button>
        <button type="button" :aria-pressed="sub === 'subs'" @click="sub = 'subs'">订阅管理 ({{ schedules.length }})</button>
        <button type="button" :aria-pressed="sub === 'history'" @click="sub = 'history'">发送历史 ({{ runs.length }})</button>
      </div>
      <div class="subbar-r">
        <t-button v-if="sub === 'subs'" theme="primary" size="small" @click="openWizard(null)">+ 新建订阅</t-button>
        <t-button v-else-if="sub === 'overview'" theme="primary" size="small" @click="openAdhocModal">选 Bot 加入队列</t-button>
      </div>
    </div>

    <div class="rep-wrap">
      <t-loading v-if="sub === 'overview'" :loading="loadingOverview">
        <div class="rep-split">
          <div class="grid14">
            <div class="rep-kpis">
              <div class="rep-kpi"><small>本期登记问题</small><b>{{ reportData?.total ?? 0 }}</b></div>
              <div class="rep-kpi"><small>待处理 / 处理中</small><b>{{ (reportData?.counts?.open ?? 0) + (reportData?.counts?.inprogress ?? 0) }}</b></div>
              <div class="rep-kpi"><small>已解决 / 已关闭</small><b>{{ (reportData?.counts?.resolved ?? 0) + (reportData?.counts?.closed ?? 0) }}</b></div>
            </div>
            <section class="panel-card">
              <div class="panel-hd"><h3>产品知识库分布</h3><small>按本期问题聚合</small></div>
              <div class="panel-bd">
                <div v-for="bar in kbDist" :key="bar.name" class="bar-row">
                  <span class="ellipsis">{{ bar.name }}</span>
                  <div class="bar-track"><div class="bar-fill" :style="{ width: `${bar.pct}%` }" /></div>
                  <b style="text-align: right">{{ bar.count }}</b>
                </div>
                <t-empty v-if="!kbDist.length" description="当前窗口暂无登记问题分布" />
              </div>
            </section>
          </div>

          <section class="panel-card">
            <div class="panel-hd">
              <h3>所见即所得 · 报告成稿</h3>
              <div class="seg">
                <button type="button" :aria-pressed="previewMode === 'msg'" @click="previewMode = 'msg'">Octo 消息</button>
                <button type="button" :aria-pressed="previewMode === 'md'" @click="previewMode = 'md'">Markdown 源码</button>
                <button type="button" :aria-pressed="previewMode === 'table'" @click="previewMode = 'table'">明细表</button>
              </div>
            </div>
            <div class="panel-bd grid12">
              <div v-if="previewMode === 'msg'" class="octo-msg-preview">
                <div class="msg-hd">
                  <b>业问 · Octo 知识运营报告</b>
                  <small>Octo 原生 Markdown 消息渲染预览 · 支持随消息附带 MD / CSV 附件</small>
                </div>
                <div class="msg-bd">
                  <OctoMarkdown :text="renderedMarkdown" />
                </div>
              </div>
              <pre v-else-if="previewMode === 'md'" class="md-pre">{{ renderedMarkdown }}</pre>
              <t-table v-else :data="reportData?.items || []" :columns="itemCols" row-key="id" size="small" />

              <t-textarea
                v-if="editNote"
                v-model="customNote"
                placeholder="补充一段给管理层的运营结论（实时同步成稿预览）..."
              />

              <div class="row-between pt6">
                <div class="row-gap">
                  <t-button size="small" variant="outline" @click="editNote = !editNote">
                    {{ editNote ? '完成补充' : '+ 补充文字结论' }}
                  </t-button>
                  <t-button size="small" variant="outline" @click="exportMd">导出 MD</t-button>
                  <t-button size="small" variant="outline" @click="exportCsv">导出 CSV</t-button>
                </div>
                <t-button theme="primary" size="small" @click="openAdhocModal">选 Bot 并加入发送队列</t-button>
              </div>
            </div>
          </section>
        </div>
      </t-loading>

      <div v-else-if="sub === 'subs'" class="grid14">
        <div class="preset-grid">
          <button v-for="p in presets" :key="p.name" type="button" class="preset-card" @click="openPreset(p)">
            <t-tag size="small" theme="primary" variant="light">推荐一键配置</t-tag>
            <b>{{ p.name }}</b>
            <small>{{ p.desc }}</small>
          </button>
        </div>
        <section class="panel-card">
          <div class="panel-hd"><h3>Octo 报告订阅列表</h3><small>支持任选发送 Bot + 多目标混选（含未绑库管理群与好友 UID）</small></div>
          <t-loading :loading="loadingSubs">
            <t-table :data="schedules" :columns="subCols" row-key="id" size="small" empty="暂无订阅，可点击上方推荐模板一键配置">
              <template #enabled="{ row }">
                <t-switch :value="row.enabled" @change="(v: boolean) => toggleSub(row, v)" />
              </template>
              <template #targets="{ row }">
                <div><b>{{ botName(row.sender_channel_id || row.channel_id) }}</b></div>
                <div class="chips mt4">
                  <t-tag v-for="t in row.delivery_targets || []" :key="t.key" size="small" variant="light">
                    {{ t.channel_kind }} · {{ t.label }}
                  </t-tag>
                  <t-tag v-if="!(row.delivery_targets?.length)" size="small" variant="outline">
                    {{ row.recipient_type === 'private' ? `私聊 · ${row.recipient_uid}` : '绑定源群' }}
                  </t-tag>
                </div>
              </template>
              <template #actions="{ row }">
                <div class="row-gap">
                  <t-button size="small" variant="outline" @click="openWizard(row)">编辑</t-button>
                  <t-button size="small" variant="text" theme="danger" @click="removeSub(row.id)">删除</t-button>
                </div>
              </template>
            </t-table>
          </t-loading>
        </section>
      </div>

      <section v-else class="panel-card">
        <div class="panel-hd">
          <h3>报告发送历史</h3>
          <div class="row-gap">
            <t-select v-model="historyStatus" size="small" style="width: 140px" :options="statusOpts" clearable placeholder="全部状态" @change="loadRuns" />
            <t-button size="small" variant="outline" @click="loadRuns">刷新</t-button>
          </div>
        </div>
        <t-loading :loading="loadingRuns">
          <t-table :data="runs" :columns="runCols" row-key="id" size="small" empty="暂无报告发送历史">
            <template #status="{ row }">
              <t-tag size="small" variant="light" :theme="runTheme(row.status)">{{ runStatusLabel(row.status) }}</t-tag>
            </template>
            <template #deliveries="{ row }">
              <span>{{ row.deliveries?.length ? `${row.deliveries.length} 个目标` : (row.target || '—') }}</span>
            </template>
            <template #actions="{ row }">
              <t-button size="small" :theme="row.status === 'pending' ? 'primary' : 'default'" variant="outline" @click="openRunDetail(row)">
                {{ row.status === 'pending' ? '审核发送' : '查看回执' }}
              </t-button>
            </template>
          </t-table>
        </t-loading>
      </section>
    </div>

    <!-- Ad-hoc Queue Modal -->
    <t-dialog v-model:visible="adhocVisible" header="选择 Octo Bot 并加入发送队列" width="680px" :confirm-btn="{ content: '加入待审核队列', loading: savingAdhoc, disabled: adhocInvalid > 0 || !adhocTargets.length }" @confirm="submitAdhoc">
      <OctoMultiTargetPicker
        :bots="bots"
        v-model:sender-channel-id="adhocBot"
        v-model:targets="adhocTargets"
        @invalid-count="adhocInvalid = $event"
      />
    </t-dialog>

    <!-- 3-Step Subscription Wizard -->
    <t-dialog v-model:visible="wizVisible" :header="wizEditingId ? '编辑 Octo 订阅' : '新建 Octo 订阅'" width="720px" :footer="false">
      <div class="wiz-body">
        <t-steps :current="wizStep - 1" readonly size="small">
          <t-step-item title="选模板与方式" />
          <t-step-item title="范围与格式" />
          <t-step-item title="发给谁与何时" />
        </t-steps>

        <div v-if="wizStep === 1" class="two-col">
          <label class="fld"><span>订阅名称</span><t-input v-model="wiz.name" placeholder="例如：每周一秒针知识运营周报" /></label>
          <label class="fld"><span>运行方式</span><t-select v-model="wiz.mode" :options="modeOpts" /></label>
          <label class="fld full"><span>报告模板</span><t-select v-model="wiz.template" :options="tplOpts" /></label>
        </div>

        <div v-else-if="wizStep === 2" class="two-col">
          <label class="fld"><span>统计周期</span><t-select v-model="wiz.period" :options="periodOpts" /></label>
          <label class="fld"><span>输出格式</span><t-select v-model="wiz.format" :options="formatOpts" /></label>
        </div>

        <div v-else class="grid12">
          <OctoMultiTargetPicker
            v-if="wiz.mode !== 'generate'"
            :bots="bots"
            v-model:sender-channel-id="wiz.senderChannelId"
            v-model:targets="wiz.targets"
            @invalid-count="wizInvalid = $event"
          />
          <t-alert v-else theme="info" :close="false">当前运行方式为「仅生成到工作台」，不会外发到 Octo 会话。</t-alert>
          <div class="two-col">
            <label class="fld"><span>频率</span><t-select v-model="wiz.frequency" :options="freqOpts" /></label>
            <label class="fld"><span>触发小时（北京时间）</span><t-input-number v-model="wiz.hour" :min="0" :max="23" theme="normal" /></label>
          </div>
          <t-checkbox v-model="wiz.enabled">保存后立即启用定时计划（取消则存为草稿）</t-checkbox>
        </div>

        <div class="wiz-ft">
          <t-button variant="outline" @click="wizVisible = false">取消</t-button>
          <t-button v-if="wizStep > 1" variant="outline" @click="wizStep--">上一步</t-button>
          <t-button v-if="wizStep < 3" theme="primary" @click="wizStep++">下一步</t-button>
          <t-button v-else theme="primary" :loading="savingWiz" @click="saveWizard">保存订阅</t-button>
        </div>
      </div>
    </t-dialog>

    <!-- Per-Target Run Receipt & Single-Target Retry Modal -->
    <t-dialog v-model:visible="runDetailVisible" :header="activeRun ? `${activeRun.schedule_name} · 逐目标回执` : '报告回执'" width="680px" :footer="false">
      <div v-if="activeRun" class="grid12">
        <div v-if="activeRun.deliveries?.length" class="grid8">
          <b style="font-size: 13px">逐目标发送回执与单目标失败重试</b>
          <div v-for="d in activeRun.deliveries" :key="d.id" class="deliv-row" :class="d.status">
            <div>
              <b>{{ d.target_label }}</b> · {{ runStatusLabel(d.status) }}
              <span v-if="d.receipt"> · 回执 {{ d.receipt }}</span>
              <div v-if="d.error_message" class="mini">{{ d.error_message }}</div>
            </div>
            <t-button
              v-if="d.status === 'failed'"
              size="small"
              theme="primary"
              :loading="retryingDeliveryId === d.id"
              @click="retryOneTarget(activeRun.id, d.id)"
            >
              仅重发此目标
            </t-button>
          </div>
        </div>
        <div class="wiz-ft">
          <t-button variant="outline" @click="runDetailVisible = false">关闭</t-button>
          <t-button v-if="activeRun.status === 'pending'" theme="primary" :loading="sendingRun" @click="approveSendRun(activeRun.id)">
            审核通过并逐目标发送
          </t-button>
        </div>
      </div>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  createAdhocReportRun,
  deleteReportSchedule,
  issueReport,
  listAllReportRuns,
  listReportSchedules,
  retryReportDelivery,
  saveReportSchedule,
  sendReportRunV3,
  type DeliveryTarget,
  type OctoIssue,
  type ReportRunViewV3,
  type ReportSchedule,
} from '@/api/octo-business'
import OctoMarkdown from '../OctoMarkdown.vue'
import OctoMultiTargetPicker from './OctoMultiTargetPicker.vue'
import { opsScope, resolveScopeQuery } from './useOctoOpsScope'

const props = defineProps<{
  kbs: Array<{ id: string; name: string }>
  bots: Array<{ id: string; name: string }>
}>()

const sub = ref<'overview' | 'subs' | 'history'>('overview')
const previewMode = ref<'msg' | 'md' | 'table'>('msg')
const editNote = ref(false)
const customNote = ref('')
const loadingOverview = ref(false)
const loadingSubs = ref(false)
const loadingRuns = ref(false)
const reportData = ref<{ counts: Record<string, number>; items: OctoIssue[]; total: number; generated_at: string; scope: string; notice: string } | null>(null)
const schedules = ref<ReportSchedule[]>([])
const runs = ref<ReportRunViewV3[]>([])
const historyStatus = ref('')

// Ad-hoc modal
const adhocVisible = ref(false)
const adhocBot = ref('')
const adhocTargets = ref<DeliveryTarget[]>([])
const adhocInvalid = ref(0)
const savingAdhoc = ref(false)

// Wizard
const wizVisible = ref(false)
const wizEditingId = ref('')
const wizStep = ref(1)
const wizInvalid = ref(0)
const savingWiz = ref(false)
const wiz = reactive({
  name: '',
  mode: 'approval',
  template: 'quality',
  period: 'previous_week',
  format: 'markdown' as 'text' | 'markdown' | 'html' | 'csv',
  frequency: 'weekly',
  hour: 9,
  enabled: false,
  senderChannelId: '',
  targets: [] as DeliveryTarget[],
})

// Run detail modal
const runDetailVisible = ref(false)
const activeRun = ref<ReportRunViewV3 | null>(null)
const sendingRun = ref(false)
const retryingDeliveryId = ref('')

const presets = [
  { name: '每周一秒针知识运营周报', desc: '每周一 09:00 汇总各产品库问答质量与未决风险，审核后多目标发送', template: 'quality', mode: 'approval', frequency: 'weekly', hour: 9 },
  { name: '每日同步失败与错答提醒', desc: '每个工作日 18:00 汇总同步异常、复核失败与待回告问题', template: 'maintenance', mode: 'approval', frequency: 'weekdays', hour: 18 },
  { name: '产品专题质量复盘', desc: '定期围绕重点知识库生成专题复盘到工作台', template: 'topic', mode: 'generate', frequency: 'interval', hour: 10 },
]

const modeOpts = [
  { value: 'approval', label: '审核后发送（推荐）' },
  { value: 'generate', label: '仅生成到工作台' },
  { value: 'auto', label: '定时自动发送' },
]
const tplOpts = [
  { value: 'quality', label: '知识质量回顾' },
  { value: 'maintenance', label: '维护待办' },
  { value: 'anomaly', label: '异常提醒' },
  { value: 'topic', label: '专题报告' },
]
const periodOpts = [
  { value: 'previous_week', label: '上一自然周' },
  { value: 'last_days', label: '滚动近 7/14 天' },
  { value: 'previous_month', label: '上一自然月' },
  { value: 'yesterday', label: '昨日' },
]
const formatOpts = [
  { value: 'markdown', label: 'Octo 原生 Markdown 消息' },
  { value: 'csv', label: 'CSV 明细表附件' },
  { value: 'html', label: 'HTML 附件' },
]
const freqOpts = [
  { value: 'weekly', label: '每周' },
  { value: 'weekdays', label: '工作日' },
  { value: 'daily', label: '每天' },
  { value: 'monthly', label: '每月' },
  { value: 'manual', label: '仅手动' },
]
const statusOpts = [
  { value: 'pending', label: '待审核' },
  { value: 'sent', label: '已发送' },
  { value: 'uncertain', label: '送达未确认' },
  { value: 'failed', label: '失败' },
]

const itemCols = [
  { colKey: 'title', title: '问题' },
  { colKey: 'status', title: '状态', width: 100 },
]
const subCols = [
  { colKey: 'name', title: '订阅名称' },
  { colKey: 'enabled', title: '启用状态', width: 100 },
  { colKey: 'targets', title: '发送 Bot 与多目标' },
  { colKey: 'frequency', title: '频率', width: 100 },
  { colKey: 'actions', title: '操作', width: 150 },
]
function fmtDateTime(v?: string) {
  if (!v) return '-'
  const d = new Date(v)
  return Number.isNaN(d.getTime())
    ? v
    : d.toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
const runCols = [
  { colKey: 'due_at', title: '生成时间', width: 170, cell: (_h: unknown, { row }: { row: ReportRunViewV3 }) => fmtDateTime(row.due_at) },
  { colKey: 'schedule_name', title: '报告名称' },
  { colKey: 'status', title: '状态', width: 110 },
  { colKey: 'deliveries', title: '发送目标', width: 180 },
  { colKey: 'actions', title: '操作', width: 120 },
]

const kbDist = computed(() => {
  const counts: Record<string, number> = {}
  for (const item of reportData.value?.items || []) {
    const name = props.kbs.find((k) => k.id === item.knowledge_base_id)?.name || item.knowledge_base_id
    counts[name] = (counts[name] || 0) + 1
  }
  const entries = Object.entries(counts).sort((a, b) => b[1] - a[1])
  const max = Math.max(1, ...entries.map((e) => e[1]))
  return entries.map(([name, count]) => ({ name, count, pct: Math.round((count / max) * 100) }))
})

const renderedMarkdown = computed(() => {
  const r = reportData.value
  const lines = [
    '# 业问 · Octo 知识运营报告',
    '',
    '## 本期概况',
    `- 登记问题总数：${r?.total ?? 0}`,
    `- 待处理：${r?.counts?.open ?? 0}，处理中：${r?.counts?.inprogress ?? 0}，已解决：${r?.counts?.resolved ?? 0}`,
  ]
  if (customNote.value.trim()) {
    lines.push('', '## 运营补充结论', `- ${customNote.value.trim()}`)
  }
  return lines.join('\n')
})

function botName(id: string) {
  return props.bots.find((b) => b.id === id)?.name || id || '默认 Bot'
}
function runTheme(st: string): 'success' | 'warning' | 'danger' | 'default' {
  if (st === 'sent') return 'success'
  if (st === 'pending') return 'warning'
  if (st === 'failed' || st === 'uncertain') return 'danger'
  return 'default'
}
function runStatusLabel(st: string) {
  return ({ sent: '已送达', pending: '待审核', uncertain: '送达未确认', failed: '确定失败', skipped: '已跳过' } as Record<string, string>)[st] || st
}

function downloadBlob(name: string, mime: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: mime }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
function exportMd() {
  downloadBlob('业问-Octo运营报告.md', 'text/markdown;charset=utf-8', renderedMarkdown.value)
}
function exportCsv() {
  const rows = ['标题,状态,产品库'].concat((reportData.value?.items || []).map((i) => `"${i.title.replace(/"/g, '""')}",${i.status},${i.knowledge_base_id}`))
  downloadBlob('业问-问题明细.csv', 'text/csv;charset=utf-8', '\ufeff' + rows.join('\r\n'))
}

async function loadOverview() {
  loadingOverview.value = true
  try {
    const q = resolveScopeQuery(opsScope)
    const res = await issueReport({ scope_id: q.scope_id || undefined, knowledge_base_id: q.knowledge_base_id || undefined, from: q.from, to: q.to })
    reportData.value = res.data
  } finally {
    loadingOverview.value = false
  }
}

async function loadSubs() {
  loadingSubs.value = true
  try {
    schedules.value = (await listReportSchedules()).data || []
  } finally {
    loadingSubs.value = false
  }
}

async function loadRuns() {
  loadingRuns.value = true
  try {
    runs.value = (await listAllReportRuns(historyStatus.value)).data || []
  } finally {
    loadingRuns.value = false
  }
}

function openAdhocModal() {
  adhocBot.value = props.bots[0]?.id || ''
  adhocTargets.value = []
  adhocVisible.value = true
}

async function submitAdhoc() {
  if (!adhocBot.value || !adhocTargets.value.length || adhocInvalid.value > 0) return
  savingAdhoc.value = true
  try {
    const q = resolveScopeQuery(opsScope)
    await createAdhocReportRun({
      title: '按需汇总报告',
      sender_channel_id: adhocBot.value,
      delivery_targets: adhocTargets.value,
      custom_note: customNote.value.trim(),
      scope_id: q.scope_id || undefined,
      knowledge_base_id: q.knowledge_base_id || undefined,
      from: q.from,
      to: q.to,
    })
    adhocVisible.value = false
    sub.value = 'history'
    MessagePlugin.success('已加入待审核发送队列')
    await loadRuns()
  } catch {
    MessagePlugin.error('加入队列失败')
  } finally {
    savingAdhoc.value = false
  }
}

function openPreset(p: (typeof presets)[number]) {
  wizEditingId.value = ''
  wizStep.value = 3
  wiz.name = p.name
  wiz.mode = p.mode
  wiz.template = p.template
  wiz.frequency = p.frequency
  wiz.hour = p.hour
  wiz.enabled = false
  wiz.senderChannelId = props.bots[0]?.id || ''
  wiz.targets = []
  wizVisible.value = true
}

function openWizard(existing: ReportSchedule | null) {
  wizEditingId.value = existing?.id || ''
  wizStep.value = 1
  wiz.name = existing?.name || ''
  wiz.mode = existing?.mode || 'approval'
  wiz.template = existing?.template || 'quality'
  wiz.period = existing?.period || 'previous_week'
  wiz.format = existing?.format || 'markdown'
  wiz.frequency = existing?.frequency || 'weekly'
  wiz.hour = existing?.hour ?? 9
  wiz.enabled = existing?.enabled ?? false
  wiz.senderChannelId = existing?.sender_channel_id || existing?.channel_id || props.bots[0]?.id || ''
  wiz.targets = [...(existing?.delivery_targets || [])]
  wizVisible.value = true
}

async function saveWizard() {
  if (wiz.mode !== 'generate' && (!wiz.targets.length || wizInvalid.value > 0)) {
    MessagePlugin.warning('请至少添加 1 个有效目标，并清理不适用当前 Bot 的目标')
    return
  }
  savingWiz.value = true
  try {
    await saveReportSchedule({
      id: wizEditingId.value,
      name: wiz.name.trim() || 'Octo 知识运营报告',
      channel_id: wiz.senderChannelId,
      sender_channel_id: wiz.senderChannelId,
      scope_id: '',
      recipient_type: 'source',
      recipient_uid: '',
      delivery_targets: wiz.targets,
      weekday: 1,
      hour: wiz.hour,
      minute: 0,
      timezone: 'Asia/Shanghai',
      format: wiz.format,
      enabled: wiz.enabled,
      template: wiz.template,
      frequency: wiz.frequency,
      period: wiz.period,
      mode: wiz.mode,
      lookback: 7,
      interval_days: 7,
      month_day: 1,
      threshold: 3,
      minimum_users: 1,
      only_changes: true,
      instruction: '',
      topic: '',
      quiet_start: 0,
      quiet_end: 0,
      cooldown_hours: 24,
    })
    wizVisible.value = false
    MessagePlugin.success(wiz.enabled ? '订阅已保存并启用' : '订阅已保存为草稿')
    await loadSubs()
  } catch {
    MessagePlugin.error('保存订阅失败，请检查配置')
  } finally {
    savingWiz.value = false
  }
}

async function toggleSub(row: ReportSchedule, on: boolean) {
  try {
    await saveReportSchedule({ ...row, enabled: on })
    await loadSubs()
  } catch {
    MessagePlugin.error('切换状态失败')
  }
}
async function removeSub(id: string) {
  await deleteReportSchedule(id)
  MessagePlugin.success('已删除订阅')
  await loadSubs()
}

function openRunDetail(row: ReportRunViewV3) {
  activeRun.value = row
  runDetailVisible.value = true
}

async function approveSendRun(runId: string) {
  sendingRun.value = true
  try {
    const res = await sendReportRunV3(runId)
    activeRun.value = res.data
    MessagePlugin.success('已完成逐目标投递')
    await loadRuns()
  } catch {
    MessagePlugin.error('发送失败或部分目标未确认，请核对回执')
    await loadRuns()
  } finally {
    sendingRun.value = false
  }
}

async function retryOneTarget(runId: string, deliveryId: string) {
  retryingDeliveryId.value = deliveryId
  try {
    const res = await retryReportDelivery(runId, deliveryId)
    activeRun.value = res.data
    MessagePlugin.success('已仅重发失败目标，未重复发送已成功/未确认目标')
    await loadRuns()
  } catch {
    MessagePlugin.error('单目标重发失败（已成功或未确认目标受保护禁止重发）')
  } finally {
    retryingDeliveryId.value = ''
  }
}

async function loadAll() {
  await Promise.allSettled([loadOverview(), loadSubs(), loadRuns()])
}
defineExpose({ reload: loadAll })
onMounted(loadAll)
</script>

<style scoped lang="less">
.rep-root { display: grid; gap: 16px; }
.rep-subbar { display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap; padding: 8px 20px; background: var(--td-bg-color-secondarycontainer); border-bottom: 1px solid var(--td-component-border); margin: -18px -22px 0; }
.rep-wrap { max-width: 1440px; margin: 0 auto; width: 100%; }
.seg { display: inline-flex; padding: 2px; border-radius: 6px; background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); }
.seg button { height: 26px; padding: 0 11px; border: 0; border-radius: 4px; background: transparent; color: var(--td-text-color-secondary); font-size: 12.5px; cursor: pointer; }
.seg button[aria-pressed='true'] { background: var(--td-brand-color-light); color: var(--td-brand-color); font-weight: 600; }
.rep-split { display: grid; grid-template-columns: minmax(0, 1.05fr) minmax(380px, 1fr); gap: 15px; align-items: start; }
.rep-kpis { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.rep-kpi { padding: 12px 14px; border-radius: 10px; background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); }
.rep-kpi small { display: block; font-size: 11.5px; color: var(--td-text-color-placeholder); }
.rep-kpi b { display: block; margin-top: 2px; font-size: 22px; }
.panel-card { background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px; overflow: hidden; }
.panel-hd { display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap; padding: 12px 16px; border-bottom: 1px solid var(--td-component-border); }
.panel-hd h3 { margin: 0; font-size: 14px; }
.panel-hd small { font-size: 12px; color: var(--td-text-color-placeholder); }
.panel-bd { padding: 14px 16px; }
.bar-row { display: grid; grid-template-columns: 140px minmax(0, 1fr) 40px; align-items: center; gap: 10px; padding: 5px 0; font-size: 12.5px; }
.bar-track { height: 8px; border-radius: 99px; background: var(--td-bg-color-secondarycontainer); overflow: hidden; }
.bar-fill { height: 100%; border-radius: 99px; background: var(--td-brand-color); }
.octo-msg-preview { border: 1px solid var(--td-brand-color-3); border-radius: 10px; overflow: hidden; }
.msg-hd { padding: 11px 14px; background: var(--td-brand-color); color: #fff; }
.msg-hd b { display: block; font-size: 14px; }
.msg-hd small { opacity: 0.88; font-size: 11.5px; }
.msg-bd { padding: 14px; }
.md-pre { margin: 0; padding: 12px; border-radius: 8px; background: var(--td-bg-color-secondarycontainer); font-size: 12px; white-space: pre-wrap; max-height: 360px; overflow: auto; }
.preset-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.preset-card { padding: 14px; border: 1px dashed var(--td-brand-color-3); border-radius: 10px; background: var(--td-bg-color-container); text-align: left; display: grid; gap: 6px; cursor: pointer; }
.preset-card:hover { border-style: solid; border-color: var(--td-brand-color); background: var(--td-brand-color-light); }
.preset-card b { font-size: 13.5px; }
.preset-card small { font-size: 12px; color: var(--td-text-color-secondary); }
.wiz-body { display: grid; gap: 16px; }
.wiz-ft { display: flex; justify-content: flex-end; gap: 8px; padding-top: 8px; border-top: 1px solid var(--td-component-border); }
.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.two-col .full { grid-column: 1 / -1; }
.fld { display: grid; gap: 5px; font-size: 12.5px; color: var(--td-text-color-secondary); }
.deliv-row { display: flex; justify-content: space-between; align-items: center; gap: 10px; padding: 9px 12px; border-radius: 8px; border: 1px solid var(--td-component-border); font-size: 12.5px; }
.deliv-row.sent { border-color: var(--td-success-color-3); background: var(--td-success-color-1); }
.deliv-row.failed, .deliv-row.uncertain { border-color: var(--td-error-color-3); background: var(--td-error-color-1); }
.grid14 { display: grid; gap: 14px; }
.grid12 { display: grid; gap: 12px; }
.grid8 { display: grid; gap: 8px; }
.row-between { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; }
.row-gap { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.chips { display: flex; flex-wrap: wrap; gap: 4px; }
.mt4 { margin-top: 4px; }
.pt6 { padding-top: 6px; border-top: 1px solid var(--td-component-border); }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mini { font-size: 12px; color: var(--td-text-color-placeholder); }
@media (max-width: 1120px) { .rep-split, .preset-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
