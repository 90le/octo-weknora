<template>
  <section class="repair" aria-label="修订与验收">
    <header>
      <h4>修订与验收</h4>
      <t-tag v-if="view?.revision" size="small">{{ states[view.state] }}</t-tag>
      <t-button variant="text" size="small" :loading="loading" :disabled="editing" @click="load">刷新资料状态</t-button>
    </header>
    <t-alert v-if="error" theme="warning">{{ error }}</t-alert>
    <template v-if="view && !editing">
      <template v-if="view.revision">
        <p><strong>{{ causes[view.plan.cause] }}</strong> · {{ view.plan.acceptance }}</p>
        <p v-if="view.plan.source" class="hint">{{ view.plan.source.path }} · 登记版本 {{ view.plan.source.version }}</p>
        <p v-if="view.published" class="hint">当前发布版本 {{ view.published.version }}</p>
        <details><summary>验收问法</summary><p>{{ view.plan.paraphrase }}</p><p>{{ view.plan.counterexample }}</p></details>
      </template>
      <t-button v-if="editable" size="small" variant="outline" @click="edit">
        {{ view.revision ? '编辑修订任务' : '认领并填写修订任务' }}
      </t-button>
    </template>
    <form v-if="editing" @submit.prevent="save">
      <label>原因<t-select v-model="form.cause" :options="causeOptions" @change="form.wait_for_update = form.cause === 'material'" /></label>
      <label>要改什么，怎样才算答对<t-textarea v-model="form.acceptance" :maxlength="1000" placeholder="填入需要核对的行、适用条件和预期结论" /></label>
      <label>关联源文件
        <t-select v-model="sourceID" filterable clearable :loading="searching" :options="options"
          @search="findFiles" placeholder="搜索本库已发布的文件夹文档" />
      </label>
      <t-checkbox v-if="sourceID" v-model="form.wait_for_update">等待该文件发布不同版本后复测</t-checkbox>
      <label>同义问法<t-textarea v-model="form.paraphrase" :maxlength="500" /></label>
      <label>条件不同的反例<t-textarea v-model="form.counterexample" :maxlength="500" /></label>
      <div class="actions">
        <t-button type="submit" theme="primary" :loading="saving" :disabled="!valid">保存修订任务</t-button>
        <t-button variant="text" :disabled="saving" @click="editing=false; error=''; load()">取消</t-button>
      </div>
    </form>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { listKnowledgeFiles } from '@/api/knowledge-base'
import { issueRepair, saveIssueRepair, type RepairPlan, type RepairView } from '@/api/octo-business'

const props = defineProps<{issueId:string; kbId:string; editable:boolean}>()
const emit = defineEmits<{loaded:[view:RepairView|null]; saved:[]}>()
const states:Record<string,string> = {ready:'可复测',waiting:'等待新版发布',needs_source:'待关联正式资料',unavailable:'原来源暂不可查'}
const causes:Record<string,string> = {material:'资料缺失或口径冲突',retrieval:'解析或检索',answer:'回答理解有误',service:'服务异常',permission:'权限或可见范围'}
const causeOptions = Object.entries(causes).map(([value,label])=>({value,label}))
const blank = ():RepairPlan => ({cause:'material',acceptance:'',paraphrase:'',counterexample:'',wait_for_update:true})
const form = reactive(blank())
const view = ref<RepairView|null>(null)
const editing = ref(false)
const saving = ref(false)
const loading = ref(false)
const searching = ref(false)
const error = ref('')
const sourceID = ref('')
const documents = ref<Array<{value:string;label:string}>>([])
const sourceOption = computed(()=>view.value?.plan.source)
const options = computed(()=>{
  const base = sourceOption.value
  if (!base || documents.value.some(row=>row.value===base.document_id)) return documents.value
  return [{value:base.document_id,label:base.path}, ...documents.value]
})
const valid = computed(()=>form.acceptance.trim() && form.paraphrase.trim() && form.counterexample.trim() &&
  form.paraphrase.trim() !== form.counterexample.trim())
let readVersion=0, searchVersion=0, alive=true
function show(value:RepairView|null) { view.value=value; emit('loaded',value) }
async function load() {
  const token=++readVersion
  loading.value=true
  try {
    const result=await issueRepair(props.issueId)
    if (alive && token===readVersion) { error.value=''; show(result.data) }
  } catch {
    if (alive && token===readVersion) { error.value='读取资料状态失败，请刷新。'; show(null) }
  } finally { if (alive && token===readVersion) loading.value=false }
}
async function findFiles(keyword='') {
  const token=++searchVersion
  searching.value=true
  try {
    const result=await listKnowledgeFiles(props.kbId,{page:1,page_size:50,keyword,source:'local_folder',parse_status:'completed'})
    if (alive && token===searchVersion) {
      const files = (result.data || []) as Array<{id:string;file_name?:string;title:string;enable_status:string;metadata?:Record<string,string>}>
      documents.value=files.filter(row=>row.enable_status==='enabled' && row.metadata?.source_version && !row.metadata?.sync_target_external_id)
        .map(row=>({value:row.id,label:row.metadata?.source_path || row.file_name || row.title}))
    }
  } catch { if (alive && token===searchVersion) error.value='读取文档列表失败，可重新输入文件名搜索。' }
  finally { if (alive && token===searchVersion) searching.value=false }
}
function edit() {
  if (!view.value) return
  Object.assign(form,view.value.revision ? view.value.plan : blank())
  sourceID.value=view.value.plan.source?.document_id || ''
  editing.value=true
  void findFiles()
}
async function save() {
  if (!valid.value || !view.value || saving.value) return
  saving.value=true
  error.value=''
  try {
    const result=await saveIssueRepair(props.issueId,{cause:form.cause,acceptance:form.acceptance,paraphrase:form.paraphrase,
      counterexample:form.counterexample,wait_for_update:Boolean(sourceID.value && form.wait_for_update),
      revision:view.value.revision,document_id:sourceID.value || ''})
    if (alive) { show(result.data); editing.value=false; emit('saved') }
  } catch { if (alive) error.value='保存失败。请核对资料仍可查；如已有他人修改，请取消并刷新后再编辑。' }
  finally { saving.value=false }
}
onMounted(load)
onBeforeUnmount(()=>{alive=false})
watch(()=>[view.value?.state,editing.value],([state,isEditing],_,cleanup)=>{
  if (isEditing || state!=='waiting') return
  const timer=window.setInterval(()=>{if (!document.hidden && !loading.value) void load()},30000)
  cleanup(()=>window.clearInterval(timer))
})
</script>

<style scoped>
.repair, form, label { display:grid; gap:8px; min-width:0 }
.repair { gap:12px; padding-bottom:16px; border-bottom:1px solid var(--td-component-border); }
header, .actions { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
header h4 { margin:0; margin-right:auto; }
p { margin:0; overflow-wrap:anywhere; white-space:pre-wrap; }
.hint { color:var(--td-text-color-secondary); }
form { gap:12px; }
.repair > button { justify-self:start; }
</style>
