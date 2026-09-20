<template>
  <t-button variant="text" @click="open">Octo 使用范围</t-button>
  <t-drawer v-model:visible="visible" header="Octo 使用范围" :footer="false" size="560px">
    <p>管理本知识库向哪些群／子区开放查询。解除查询绑定不会删除资料或撤销已有维护授权；维护授权在区域设置中单独管理。</p>
    <t-alert theme="info">下方列出直接绑定。子区可继承主群知识库，继承结果在区域设置查看。</t-alert>
    <t-loading :loading="loading">
      <p v-if="error" role="alert">{{ error }} <t-button variant="text" @click="load">重试</t-button></p>
      <t-empty v-else-if="!loading && !rows.length" description="此知识库尚未绑定 Octo 区域" />
      <ul><li v-for="s in rows" :key="s.scope_id"><div><strong>{{ s.display_name }}</strong> <t-tag size="small">{{ s.subarea_id ? '子区' : '主群' }}</t-tag><p>Bot：{{ s.account_id }}</p></div><div class="actions"><router-link :to="{ path:'/platform/settings',query:{section:'integration-octo',scope:s.scope_id} }">区域设置</router-link><t-popconfirm content="仅解除这个区域的直接绑定，不删除知识库。子区如继承了主群绑定，仍可通过主群获得查询权限。" @confirm="remove(s.scope_id)"><t-button theme="danger" variant="text" :disabled="saving">解除绑定</t-button></t-popconfirm></div></li></ul>
    </t-loading>
    <label>增加使用区域<t-select v-model="selectedScope" :options="options" filterable clearable placeholder="选择群或子区" /></label>
    <t-button :loading="saving" :disabled="!selectedScope || loading" @click="add">绑定到所选区域</t-button>
    <p><router-link :to="{ name: 'octoGroups' }">接入新的群／子区</router-link></p>
    <p><router-link :to="{ name: 'knowledgeContacts', params: { kbId } }">管理此知识库的联系人</router-link></p>
  </t-drawer>
</template>
<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { get } from '@/utils/request'
import { listScopes, bindKB, unbindKB, type OctoScope } from '@/api/octo'
import { MessagePlugin } from 'tdesign-vue-next'
const props = defineProps<{ kbId: string }>()
const visible = ref(false), loading = ref(false), saving=ref(false), error = ref(''), selectedScope=ref('')
const scopes=ref<OctoScope[]>([])
const rows = ref<Array<{ scope_id: string; display_name: string; account_id: string; group_id: string; subarea_id: string }>>([])
const options=computed(()=>scopes.value.filter(s=>!rows.value.some(r=>r.scope_id===s.id)).map(s=>({value:s.id,label:`${s.subarea_id?'子区':'主群'} · ${s.display_name} · ${s.account_id}`})))
let version = 0
watch(() => props.kbId, () => { version++; visible.value = false; rows.value = []; scopes.value=[]; selectedScope.value='' })
watch(visible, open=>{if(!open) version++})
onBeforeUnmount(()=>{version++})
async function open() { visible.value = true; await load() }
async function load() {
  const v = ++version; loading.value = true; error.value = ''; rows.value = []; scopes.value=[]
  try {
    const r = await get(`/api/v1/octo/knowledge-bases/${encodeURIComponent(props.kbId)}/scopes`)
    let offset=0; const all:OctoScope[]=[]
    while(v===version){const batch=await listScopes(offset); all.push(...batch.data); if(batch.data.length<100) break; offset+=100}
    if(v===version){rows.value=r.data; scopes.value=all}
  } catch { if (v === version) error.value = '使用范围读取失败，请检查当前工作区权限。' }
  finally { if (v === version) loading.value = false }
}
async function change(operation:()=>Promise<unknown>){if(saving.value)return;saving.value=true;try{await operation();selectedScope.value='';await load();await MessagePlugin.success('使用范围已更新')}catch{await MessagePlugin.error('更新失败，请检查权限并重试')}finally{saving.value=false}}
function add(){const id=selectedScope.value,kb=props.kbId;if(id)return change(()=>bindKB(id,kb))}
function remove(id:string){const kb=props.kbId;return change(()=>unbindKB(id,kb))}
</script>
<style scoped>li { padding: 16px 0; overflow-wrap: anywhere; border-bottom:1px solid var(--td-component-border); display:flex;justify-content:space-between;gap:12px; } p { color: var(--td-text-color-secondary); line-height: 1.6; } ul { padding:0;list-style:none; } label{display:grid;gap:8px;margin:20px 0 12px}.actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}</style>
