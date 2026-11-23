<template>
  <section class="retests">
    <OctoIssueRepair :issue-id="issueId" :kb-id="kbId" :editable="editable"
      @loaded="view => repair = view" @saved="emit('repair-saved')" />
    <div class="actions">
      <h4>复测历史</h4>
      <t-button variant="text" :loading="loading" @click="refresh">刷新</t-button>
      <t-select v-if="repair?.revision && canRun" v-model="scenario" :options="scenarioOptions" aria-label="复测题目" style="width: 140px" />
      <t-button v-if="canRun" variant="outline" :loading="running" :disabled="activeRun || !repair || !['unclaimed','ready'].includes(repair.state)" @click="run">
        {{ repair?.revision ? '运行复测' : '复测原问题' }}
      </t-button>
    </div>
    <p v-if="canRun" class="hint">使用当前智能体和关联知识库。核验多轮问题时，请同时参考原会话。</p>
    <t-alert v-if="error" theme="warning">{{ error }}</t-alert>
    <p v-if="!loading && !records.length && !error" class="hint">暂无复测记录</p>
    <details v-for="item in records" :key="item.id" :open="item.id === opened" @toggle="($event)=>toggle(item.id,$event)">
      <summary>
        {{ new Date(item.created_at).toLocaleString() }} · {{ scenarioNames[item.scenario || 'original'] }} · {{ states[item.state] || item.state }}
        <span v-if="item.state==='completed'"> · {{ decisions[item.review_state] || '待人工核验' }}</span>
      </summary>
      <p v-if="item.error_code" class="hint">{{ failures[item.error_code] || '运行失败，可重新复测。' }}</p>
      <template v-if="item.state==='completed'">
        <p v-if="item.question"><strong>复测提问</strong> {{ item.question }}</p>
        <p v-if="item.source_version" class="hint">资料版本 {{ item.source_version }} · {{ item.document_cited ? '答复已引用该版本' : '答复未返回该版本的文档引用' }}</p>
        <OctoMarkdown :text="item.answer" />
        <p v-if="item.model_id" class="hint">模型 {{ item.model_id }}</p>
        <p v-if="item.sources?.length" class="hint">查阅资料 {{ item.sources.join('、') }}</p>
        <template v-if="item.review_state">
          <OctoMarkdown :text="item.review_note" />
          <p v-if="item.reviewed_at" class="hint">核验时间 {{ new Date(item.reviewed_at).toLocaleString() }}</p>
        </template>
        <t-button v-else variant="outline" size="small" @click="openReview(item.id)">记录人工结论</t-button>
      </template>
    </details>
    <t-dialog v-model:visible="reviewOpen" attach="body" header="记录复测结论"
      :confirm-btn="{content:'保存结论',loading:reviewing}" @confirm="review">
      <div class="review">
        <t-select v-model="decision" :options="decisionOptions" aria-label="核验结论" />
        <t-textarea v-model="note" :maxlength="1000" placeholder="填写核验依据或后续需修复的内容" />
        <p class="hint">问题状态可在「更新处理」中另行调整。</p>
        <t-alert v-if="reviewError" theme="warning">{{ reviewError }}</t-alert>
      </div>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { issueRetests, retestIssue, reviewIssueRetest, type IssueRetestRecord, type RepairView } from '@/api/octo-business'
import OctoIssueRepair from './OctoIssueRepair.vue'
import OctoMarkdown from './OctoMarkdown.vue'

const props = defineProps<{issueId:string;kbId:string;canRun:boolean;editable:boolean}>()
const emit = defineEmits<{'repair-saved':[]}>()
const repair = ref<RepairView | null>(null)
const scenario = ref('original')
const scenarioNames:Record<string,string> = {original:'原问题',paraphrase:'同义问法',counterexample:'条件反例'}
const scenarioOptions = Object.entries(scenarioNames).map(([value,label])=>({value,label}))
const records = ref<IssueRetestRecord[]>([])
const loading = ref(false)
const running = ref(false)
const opened = ref('')
const error = ref('')
const reviewOpen = ref(false)
const reviewing = ref(false)
const runID = ref('')
const decision = ref('verified')
const note = ref('')
const reviewError = ref('')
const states:Record<string,string> = {running:'进行中',completed:'已完成',failed:'失败',interrupted:'已中断'}
const decisions:Record<string,string> = {verified:'核验通过',unresolved:'仍未修复',insufficient:'依据不足'}
const failures:Record<string,string> = {
  interrupted:'复测中断，可以重新运行。',source_changed:'原消息或知识范围在复测期间失效。',
  execution:'模型或工具未返回完整答复，可以重新复测。',
}
const activeRun = computed(()=>records.value.some(item=>item.state==='running'))
const decisionOptions = Object.entries(decisions).map(([value,label])=>({value,label}))
let revision=0

async function load() {
  const request=++revision
  loading.value=true
  try {
    const response=await issueRetests(props.issueId)
    if(request===revision) records.value=response.data
  } catch {
    if(request===revision) error.value ||= '读取复测历史失败。'
  } finally { if(request===revision) loading.value=false }
}
function refresh() { error.value=''; void load() }
async function run() {
  if(running.value || activeRun.value) return
  running.value=true
  error.value=''
  try { opened.value=(await retestIssue(props.issueId,scenario.value,repair.value?.revision || 0)).data.id }
  catch { error.value='复测未完成。可刷新查看记录，或检查原问题、知识范围和模型。' }
  finally { await load(); running.value=false }
}
function toggle(id:string,event:Event) {
  if ((event.target as HTMLDetailsElement).open) opened.value=id
  else if(opened.value===id) opened.value=''
}
function openReview(id:string) {
  runID.value=id; decision.value='verified'; note.value=''; reviewError.value=''; reviewOpen.value=true
}
async function review() {
  if(reviewing.value) return
  if(!note.value.trim()) { reviewError.value='请填写核验依据。'; return }
  reviewing.value=true
  reviewError.value=''
  try {
    await reviewIssueRetest(props.issueId,runID.value,decision.value,note.value)
    reviewOpen.value=false
    await load()
  } catch { reviewError.value='结论保存失败。请刷新核对本次复测是否已由其他人核验。' }
  finally { reviewing.value=false }
}
onMounted(load)
watch(activeRun, (active, _, cleanup) => {
  if (!active) return
  const timer=window.setInterval(()=>{if(!loading.value) void load()},5000)
  cleanup(()=>window.clearInterval(timer))
})
onBeforeUnmount(()=>{ revision++ })
</script>

<style scoped>
.retests, .review { display:grid; gap:12px; }
.retests { border-top:1px solid var(--td-component-border); padding-block:16px; }
.actions { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
h4, p { margin:0; }
.actions h4 { margin-right:auto; }
details { border:1px solid var(--td-component-border); border-radius:var(--app-radius-md); padding:10px 12px; }
details > * + * { margin-top:10px; }
summary { cursor:pointer; overflow-wrap:anywhere; }
.hint { color:var(--td-text-color-secondary); overflow-wrap:anywhere; }
</style>
