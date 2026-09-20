<template>
  <t-dialog v-model:visible="visible" header="接入群或子区" :confirm-loading="saving" :confirm-btn="{ content: '接入区域', disabled: !account || !group || (kind === 'thread' && !thread) }" @confirm="save">
    <p class="hint">只展示所选 Bot 可访问的真实区域。接入后绑定知识库才会响应；不会创建或删除 Octo 群。</p>
    <label>Bot 连接<t-select v-model="account" :options="connections.map(c => ({label:c.account_id,value:c.account_id}))" @change="loadGroups" /></label>
    <label>区域类型<t-radio-group v-model="kind"><t-radio-button value="group">主群</t-radio-button><t-radio-button value="thread">子区</t-radio-button></t-radio-group></label>
    <label>Octo 群<t-select v-model="group" :loading="loading" :options="groupOptions" filterable placeholder="选择真实群名" @change="loadThreads" /></label>
    <label v-if="kind === 'thread'">子区<t-select v-model="thread" :loading="loading" :options="threads.map(s=>({label:s.name,value:s.subarea_id}))" filterable placeholder="选择真实子区名" /></label>
    <p v-if="kind === 'thread'" class="hint">先接入主群，再接入该群子区。Bot 需要已经加入子区；继承知识范围可在接入后设置。</p>
    <t-button v-if="kind === 'thread' && hasMoreThreads" variant="text" :loading="loading" @click="loadThreadsPage">加载更多子区</t-button>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <t-button variant="text" :disabled="!account" :loading="loading" @click="kind === 'thread' && group ? loadThreads() : loadGroups()">重新读取平台区域</t-button>
  </t-dialog>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { availableScopes, createScope, type AvailableOctoScope, type OctoScope } from '@/api/octo'
const visible = defineModel<boolean>('visible', { default: false })
const props = defineProps<{ connections: Array<{account_id:string}>; scopes: OctoScope[] }>()
const emit = defineEmits<{saved:[scope:OctoScope]}>()
const account = ref(''), group = ref(''), thread = ref(''), kind = ref('group'), error = ref('')
const groups = ref<AvailableOctoScope[]>([]), threads = ref<AvailableOctoScope[]>([])
const loading = ref(false), saving = ref(false), hasMoreThreads = ref(false)
let version = 0, threadPage = 1
const groupOptions = computed(() => groups.value.filter(g => kind.value === 'group' || props.scopes.some(s=>s.account_id === account.value && s.group_id === g.group_id && !s.subarea_id)).map(g=>({label:g.name,value:g.group_id})))
watch(kind, () => { group.value = ''; thread.value = ''; threads.value = []; hasMoreThreads.value=false })
watch(visible, (open) => { version++; loading.value=false; error.value=''; if (!open) {account.value=''; group.value=''; thread.value=''; groups.value=[]; threads.value=[]} else if (props.connections.length === 1) {account.value=props.connections[0]!.account_id; void loadGroups()} })
async function loadGroups() {
  const v=++version; group.value=''; thread.value=''; groups.value=[]; threads.value=[]; error.value=''; if (!account.value) return; loading.value=true
  try {const result=await availableScopes(account.value); if(v===version) groups.value=result.data}
  catch {if(v===version) error.value='群列表读取失败，请检查 Bot 是否已入群及连接是否有效。'} finally {if(v===version) loading.value=false}
}
async function loadThreads() {thread.value=''; threads.value=[]; threadPage=1; hasMoreThreads.value=false; await loadThreadsPage()}
async function loadThreadsPage() {
  const v=++version; if(kind.value!=='thread'||!account.value||!group.value) return; loading.value=true; error.value=''
  const selectedGroup=group.value
  try {const result=await availableScopes(account.value,selectedGroup,threadPage); if(v===version&&group.value===selectedGroup&&kind.value==='thread'){threads.value.push(...result.data); hasMoreThreads.value=result.data.length===100; threadPage++}}
  catch {if(v===version) error.value='子区列表读取失败。请确认 Bot 可以访问该群。'} finally {if(v===version) loading.value=false}
}
async function save() {
  if(saving.value||!account.value||!group.value||(kind.value==='thread'&&!thread.value)) return
  saving.value=true; error.value=''
  try {const result=await createScope({account_id:account.value,group_id:group.value,subarea_id:kind.value==='thread'?thread.value:'',inherit_parent:false}); emit('saved',result.data); visible.value=false}
  catch {error.value='接入失败。该区域可能已经登记，或 Bot 尚未加入子区；请核对后重试。'} finally {saving.value=false}
}
</script>
<style scoped>label{display:grid;gap:8px;margin:18px 0}.hint{color:var(--td-text-color-secondary);font-size:13px;line-height:1.65}</style>
