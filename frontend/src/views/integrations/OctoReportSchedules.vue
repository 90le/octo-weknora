<template>
  <section class="subscriptions">
    <div class="toolbar">
      <h3>报告订阅</h3>
      <t-button @click="open()">新建订阅</t-button>
      <t-button variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <t-loading :loading="loading">
      <t-empty v-if="!loading && !rows.length && !error" description="暂无报告订阅" />
      <div class="cards">
        <article v-for="row in rows" :key="row.id" class="card">
          <div class="row"><strong>{{ row.name }}</strong><t-tag :theme="row.enabled?'success':'default'">{{ row.enabled?'已启用':'已停用' }}</t-tag></div>
          <p>{{ scopeName(row.scope_id) }} · {{ label(templates,row.template,'旧周报') }} · {{ label(frequencies,row.frequency,'每周') }}</p>
          <p>{{ label(modes,row.mode,'自动发送') }} · {{ targetLabel(row) }}</p>
          <p v-if="validTime(row.next_run_at) && row.enabled">下次运行 {{ time(row.next_run_at) }}</p>
          <p v-if="row.last_run_at">上次运行 {{ time(row.last_run_at) }} · {{ statusLabel(row.last_status) }}</p>
          <t-alert v-if="row.last_error" theme="warning">{{ row.last_error }}</t-alert>
          <div class="row">
            <t-button variant="text" @click="open(row)">编辑</t-button>
            <t-button variant="text" @click="showHistory(row)">报告历史</t-button>
            <t-popconfirm content="删除订阅将停止后续运行。确定删除？" @confirm="remove(row.id)">
              <t-button theme="danger" variant="text" :disabled="saving">删除</t-button>
            </t-popconfirm>
          </div>
        </article>
      </div>
    </t-loading>

    <t-dialog v-model:visible="visible" :header="draft.id?'编辑报告订阅':'新建报告订阅'" width="min(700px, 94vw)"
      :confirm-btn="{content:draft.enabled?'保存并启用':'保存草稿',loading:saving}" @confirm="save">
      <div class="editor">
        <label>名称<t-input v-model="draft.name" :maxlength="100" /></label>
        <div class="fields">
          <label>模板<t-select v-model="draft.template" :options="draft.id ? [{value:'',label:'旧周报'},...templates] : templates" /></label>
          <label>运行方式<t-select v-model="draft.mode" :options="modes" /></label>
          <label>Bot 渠道<t-select v-model="draft.channel_id" :options="channelOptions" filterable @change="changeChannel" /></label>
          <label>统计来源<t-select v-model="draft.scope_id" :loading="channelLoading" :options="scopeOptions" filterable /></label>
        </div>
        <div v-if="draft.mode!=='generate'" class="fields">
          <label>发送目标<t-select v-model="draft.recipient_type" :options="[{value:'source',label:'原群／子区'},{value:'private',label:'授权私聊'}]" /></label>
          <label v-if="draft.recipient_type==='private'">授权接收人<t-select v-model="draft.recipient_uid" :options="dmUIDs.map(uid=>({value:uid,label:uid}))" filterable /></label>
          <label>格式<t-select v-model="draft.format" :options="formats" /></label>
        </div>
        <div class="fields">
          <label>频率<t-select v-model="draft.frequency" :options="frequencies" /></label>
          <label>统计周期<t-select v-model="draft.period" :options="draft.frequency==='manual'?periods:periods.filter(o=>o.value!=='custom')" /></label>
          <label v-if="draft.frequency==='weekly'">星期<t-select v-model="draft.weekday" :options="weekdays.map((label,value)=>({label,value}))" /></label>
          <label v-if="draft.frequency==='monthly'">每月日期<t-input-number v-model="draft.month_day" :min="1" :max="31" /></label>
          <label v-if="draft.frequency==='interval'">间隔天数<t-input-number v-model="draft.interval_days" :min="1" :max="90" /></label>
          <label v-if="draft.period==='last_days' || draft.period==='since_last'">滚动天数<t-input-number v-model="draft.lookback" :min="1" :max="93" /></label>
          <label v-if="draft.frequency!=='manual'">小时（北京时间）<t-input-number v-model="draft.hour" :min="0" :max="23" /></label>
          <label v-if="draft.frequency!=='manual'">分钟<t-input-number v-model="draft.minute" :min="0" :max="59" /></label>
          <template v-if="draft.period==='custom'">
            <label>预览起始日期<t-input v-model="previewFrom" type="date" /></label>
            <label>预览结束日期<t-input v-model="previewTo" type="date" /></label>
          </template>
          <template v-if="draft.template==='anomaly'">
            <label>最少待关注次数<t-input-number v-model="draft.threshold" :min="1" /></label>
            <label>最少提问人数<t-input-number v-model="draft.minimum_users" :min="1" /></label>
          </template>
        </div>
        <label v-if="draft.template">补充分析指令<t-textarea v-model="draft.instruction" :maxlength="750"
          placeholder="例如优先核对引用异常，建议按维护优先次序排序" />
          <small>使用已配置的复核模型排序建议。报告数字和发送目标由系统确定。</small>
        </label>
        <label v-if="draft.template" class="row"><t-switch v-model="draft.only_changes" />无新增或指标重复时跳过</label>
        <label class="row"><t-switch v-model="draft.enabled" />启用计划</label>
        <div class="row"><t-button variant="outline" :loading="checking" @click="preview">核验并预览周期</t-button></div>
        <t-alert v-if="previewResult" :theme="previewResult.authorized?'success':'warning'">
          {{ previewResult.authorized?'统计范围和目标已核验':diagnosis(previewResult.code) }}
          <div>统计区间 {{ time(previewResult.from) }} 至 {{ time(previewResult.to) }}（不含结束时刻）</div>
          <div v-if="validTime(previewResult.next)">下次运行 {{ time(previewResult.next) }}</div>
        </t-alert>
        <t-alert v-if="formError" theme="error">{{ formError }}</t-alert>
      </div>
    </t-dialog>

    <t-drawer v-model:visible="historyVisible" size="min(760px, 100vw)" :footer="false" :header="historyPlan?.name || '报告历史'">
      <div v-if="historyPlan" class="editor">
        <div v-if="historyPlan.period==='custom'" class="fields">
          <label>起始日期<t-input v-model="runFrom" type="date" /></label>
          <label>结束日期<t-input v-model="runTo" type="date" /></label>
        </div>
        <div class="row">
          <t-button :loading="generating" @click="generate">手动生成</t-button>
          <t-button variant="outline" :loading="runsLoading" @click="loadRuns">刷新历史</t-button>
          <t-select v-model="runFilter" :options="historyFilters" aria-label="筛选报告状态" style="max-width:180px" />
        </div>
        <t-alert v-if="runError" theme="warning">{{ runError }}</t-alert>
        <t-empty v-if="!activeRuns.length && !runsLoading" description="暂无此状态的报告" />
        <div v-for="run in activeRuns" :key="run.id" class="card">
          <div class="row"><strong>{{ time(run.due_at) }}</strong><t-tag>{{ statusLabel(run.status) }}</t-tag></div>
          <p v-if="run.from">区间 {{ time(run.from) }} 至 {{ time(run.to) }} · {{ run.target }}</p>
          <p v-if="run.error_code">{{ diagnosis(run.error_code) }}</p>
          <t-button v-if="run.from" size="small" variant="outline" @click="openRun(run)">打开报告</t-button>
        </div>
      </div>
    </t-drawer>
    <t-dialog v-model:visible="runVisible" header="报告详情" width="min(720px, 94vw)" :footer="false">
      <div v-if="activeRun" class="editor">
        <div class="row"><t-tag>{{ statusLabel(activeRun.status) }}</t-tag><span>{{ activeRun.target }}</span></div>
        <OctoMarkdown :text="activeRun.summary" empty="本次运行没有可打开的报告正文" />
        <div class="row">
          <t-button v-if="activeRun.summary" variant="outline" @click="download">下载 Markdown</t-button>
          <t-popconfirm v-if="activeRun.status==='pending'" :content="'确认使用当前订阅发送到'+activeRun.target+'？'" @confirm="send">
            <t-button :loading="sending">审核通过并发送</t-button>
          </t-popconfirm>
        </div>
      </div>
      <t-alert v-if="detailError" theme="warning">{{ detailError }}</t-alert>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listAllIMChannels, listIMChannels, type IMChannelOverview } from '@/api/agent'
import {
  deleteReportSchedule, deliverReportRun, detailReportRun, generateReportRun, listReportRuns, listReportSchedules,
  previewReportSchedule, saveReportSchedule, type ReportDates, type ReportPreview, type ReportRun, type ReportSchedule,
} from '@/api/octo-business'
import type { OctoScope } from '@/api/octo'
import { useAuthStore } from '@/stores/auth'
import OctoMarkdown from './OctoMarkdown.vue'
import { scopePath } from './octoBusinessView'

const props = defineProps<{scopes:OctoScope[]}>()
const auth = useAuthStore()
const rows = ref<ReportSchedule[]>([])
const channels = ref<IMChannelOverview[]>([])
const loading = ref(false)
const saving = ref(false)
const visible = ref(false)
const error = ref('')
const formError = ref('')
const channelLoading = ref(false)
const account = ref('')
const dmUIDs = ref<string[]>([])
const checking = ref(false)
const previewFrom = ref('')
const previewTo = ref('')
const previewResult = ref<ReportPreview>()
const weekdays = ['周日','周一','周二','周三','周四','周五','周六']
const options = (entries:Record<string,string>) => Object.entries(entries).map(([value,label]) => ({value,label}))
const templates = options({maintenance:'维护待办',quality:'知识质量回顾',topic:'专题报告',anomaly:'异常提醒'})
const modes = options({generate:'仅生成',approval:'审核后发送',auto:'自动发送'})
const frequencies = options({manual:'手动',daily:'每天',weekdays:'工作日',weekly:'每周',monthly:'每月',interval:'按天数间隔'})
const periods = options({last_days:'滚动天数',yesterday:'昨日',previous_week:'上一自然周',previous_month:'上一自然月',since_last:'上期截止至本期',custom:'自选日期'})
const formats = options({text:'聊天文本',markdown:'Markdown 文件',html:'HTML 文件',csv:'CSV 表格'})
const states:Record<string,string> = {sent:'已发送',generated:'已生成',pending:'待审核',skipped:'已跳过',failed:'失败',generating:'生成中',sending:'发送中',uncertain:'送达未确认',queue_failed:'任务暂未入队'}
const historyFilters = [{value:'',label:'全部状态'},...options(states)]
const diagnostics:Record<string,string> = {
  channel:'该 Bot 渠道已停用、已移除或尚未连接。',connection:'该 Bot 的连接凭据已变更或身份未核验。',
  scope:'该区域可能已删除、未核验或属于另一个 Bot。',scope_live:'Octo 无法核验这个群或子区的当前状态。',
  binding:'这个区域没有可用的知识绑定。',recipient_policy:'接收人没有明确的报告私聊授权，或被当前私聊策略拒绝。',
  recipient_membership:'无法核验这位接收人仍属于来源群或子区。',plan_changed:'订阅配置已经变更，需要按新配置生成。',
  delivery_unconfirmed:'平台送达未获确认，请先核对目标会话。',authorization:'渠道、来源或接收权限已变更。',
}
const diagnosis = (key:string) => diagnostics[key] || '请核对当前配置和权限。'
const statusLabel = (s?:string) => states[s || ''] || s || '尚无结果'
const label = (values:{value:string;label:string}[],value:string,fallback:string) => values.find(item=>item.value===value)?.label || fallback
const blank = ():ReportSchedule => ({
  id:'',name:'',channel_id:'',scope_id:'',recipient_type:'source',recipient_uid:'',weekday:1,hour:9,minute:0,
  timezone:'Asia/Shanghai',format:'text',enabled:false,template:'quality',frequency:'weekly',period:'previous_week',
  mode:'generate',lookback:7,interval_days:7,month_day:1,threshold:3,minimum_users:2,only_changes:false,instruction:'',
})
const draft = ref(blank())
const channelOptions = computed(() => channels.value.map(c => ({value:c.id,label:(c.name || c.bot_identity) + (c.enabled?'':'（已停用）')})))
const scopeOptions = computed(() => props.scopes.filter(s => s.account_id === account.value).map(s => ({
  label: scopePath(s,props.scopes) + (s.sync_status==='verified'?'':'（未核验）'), value:s.id,
})))
const scopeName = (id:string) => { const scope=props.scopes.find(s=>s.id===id); return scope?scopePath(scope,props.scopes):id }
const targetLabel = (plan:ReportSchedule) => plan.mode==='generate'?'工作台':plan.recipient_type==='source'?'原群／子区':'授权私聊'
const time = (value?:string) => value ? new Date(value).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}) : '—'
const validTime = (value?:string) => Boolean(value && !value.startsWith('0001-'))
const dates = (from:string,to:string):ReportDates => ({
  from:from?from+'T00:00:00+08:00':undefined,
  to:to?new Date(new Date(to+'T00:00:00+08:00').getTime()+86400000).toISOString():undefined,
})
let version=0,channelVersion=0
async function load() {
  const v=++version; loading.value=true; error.value=''
  try {
    const [r,c]=await Promise.all([listReportSchedules(),listAllIMChannels()])
    if(v===version){rows.value=r.data; channels.value=(c.data||[]).filter(c=>c.platform==='octo')}
  } catch { if(v===version) error.value='报告订阅读取失败。' }
  finally { if(v===version) loading.value=false }
}
async function loadChannel() {
  const v=++channelVersion; account.value=''; dmUIDs.value=[]; channelLoading.value=false
  const channel=channels.value.find(c=>c.id===draft.value.channel_id)
  if(!channel) return
  channelLoading.value=true
  try {
    const result=await listIMChannels(channel.agent_id)
    if(v!==channelVersion) return
    const cfg=result.data.find(item=>item.id===channel.id)?.public_config||{}
    account.value=String(cfg.account_id||'')
    dmUIDs.value=Array.isArray(cfg.allowed_dm_uids)?cfg.allowed_dm_uids:[]
  } catch { if(v===channelVersion) formError.value='渠道配置读取失败。' }
  finally { if(v===channelVersion) channelLoading.value=false }
}
function changeChannel() { draft.value.scope_id=''; draft.value.recipient_uid=''; void loadChannel() }
function open(row?:ReportSchedule) {
  draft.value=row?{...blank(),...row,frequency:row.frequency||'weekly',period:row.period||'last_days',mode:row.mode||'auto',lookback:row.lookback||7}:blank()
  visible.value=true; formError.value=''; account.value=''; dmUIDs.value=[]; previewFrom.value=''; previewTo.value=''
  if(row) void loadChannel()
}
async function preview() {
  if(checking.value || !draft.value.name || !draft.value.channel_id || !draft.value.scope_id){
    formError.value='请填写名称并选择 Bot 和统计区域。'; return false
  }
  checking.value=true; formError.value=''
  try {
    previewResult.value=(await previewReportSchedule({...draft.value,...dates(previewFrom.value,previewTo.value)})).data
    return previewResult.value.authorized
  } catch { formError.value='无法预览。请检查频率、统计周期、日期和接收人。'; return false }
  finally { checking.value=false }
}
async function save() {
  if(saving.value) return
  saving.value=true; formError.value=''
  try {
    if(draft.value.enabled && !(await preview()) && draft.value.period!=='custom') return
    await saveReportSchedule({...draft.value})
    visible.value=false
    await load()
    await MessagePlugin.success('报告订阅已保存')
  } catch { formError.value='保存失败。请核对字段；停用的配置可先保存为草稿。' }
  finally { saving.value=false }
}
async function remove(id:string) {
  if(saving.value)return
  saving.value=true
  try{await deleteReportSchedule(id);await load()}
  catch{await MessagePlugin.error('删除订阅失败。')}
  finally{saving.value=false}
}
watch(draft,()=>{previewResult.value=undefined},{deep:true})
watch(()=>draft.value.frequency,value=>{if(value!=='manual'&&draft.value.period==='custom') draft.value.period='last_days'})
watch([previewFrom,previewTo],()=>{previewResult.value=undefined})

const historyVisible=ref(false), historyPlan=ref<ReportSchedule>()
const runs=ref<ReportRun[]>([]), runsLoading=ref(false), generating=ref(false), sending=ref(false)
const runFilter=ref(''), runFrom=ref(''), runTo=ref(''), runError=ref(''), detailError=ref('')
const runVisible=ref(false), activeRun=ref<ReportRun>()
const activeRuns=computed(()=>runs.value.filter(run=>!runFilter.value||run.status===runFilter.value))
async function showHistory(plan:ReportSchedule) {
  historyPlan.value=plan; historyVisible.value=true; runFilter.value=''; runs.value=[]; runFrom.value=''; runTo.value=''
  await loadRuns()
}
async function loadRuns() {
  const plan=historyPlan.value
  if(!plan) return
  runsLoading.value=true; runError.value=''
  try { const r=await listReportRuns(plan.id); if(historyPlan.value?.id===plan.id) runs.value=r.data }
  catch { if(historyPlan.value?.id===plan.id) runError.value='报告历史读取失败。' }
  finally { if(historyPlan.value?.id===plan.id) runsLoading.value=false }
}
async function generate() {
  const plan=historyPlan.value
  if(!plan||generating.value) return
  if(plan.period==='custom' && (!runFrom.value || !runTo.value)) {runError.value='请选择需补生成的日期区间。';return}
  generating.value=true;runError.value=''
  try {
    const result=await generateReportRun(plan.id,dates(runFrom.value,runTo.value))
    if(historyPlan.value?.id!==plan.id) return
    await loadRuns()
    if(result.data.summary) await openRun(result.data)
  } catch { if(historyPlan.value?.id===plan.id) runError.value='生成失败。请检查来源授权和统计日期。' }
  finally { generating.value=false }
}
async function openRun(run:ReportRun) {
  activeRun.value=undefined; detailError.value=''; runVisible.value=true
  try { activeRun.value=(await detailReportRun(run.schedule_id,run.id)).data }
  catch { detailError.value='报告正文无法读取，请检查当前渠道和统计来源权限。' }
}
function download() {
  if(!activeRun.value?.summary) return
  const url=URL.createObjectURL(new Blob([activeRun.value.summary],{type:'text/markdown;charset=utf-8'}))
  const anchor=document.createElement('a')
  anchor.href=url; anchor.download='knowledge-operations.md'; anchor.click()
  window.setTimeout(()=>URL.revokeObjectURL(url),1000)
}
async function send() {
  const run=activeRun.value
  if(!run||sending.value) return
  sending.value=true;detailError.value=''
  try {
    activeRun.value=(await deliverReportRun(run.schedule_id,run.id)).data
    await loadRuns()
  } catch { detailError.value='未能确认发送。请先查阅报告历史和目标会话。' }
  finally { sending.value=false }
}
watch(()=>auth.currentTenantId,()=>{
  version++;channelVersion++;rows.value=[];channels.value=[];visible.value=false;historyVisible.value=false
  runVisible.value=false;historyPlan.value=undefined;void load()
})
onMounted(load)
onBeforeUnmount(()=>{version++;channelVersion++})
</script>

<style scoped>
.subscriptions, .editor { display:grid; gap:14px; }
.toolbar, .row { display:flex; gap:10px; align-items:center; flex-wrap:wrap; }
.toolbar h3 { margin:0 auto 0 0; }
.cards, .fields { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,250px),1fr)); gap:12px; }
.card { border:1px solid var(--td-component-border); border-radius:var(--app-radius-md); padding:14px; }
p, small { color:var(--td-text-color-secondary); }
label { display:grid; gap:6px; }
label.row { display:flex; }
</style>
