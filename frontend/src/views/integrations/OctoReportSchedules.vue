<template>
  <section>
    <div class="toolbar"><div><h3>每周问题汇总</h3><p>统计指定区域最近 7 天登记的问题。只有明确启用的计划会自动发送。</p></div><t-button @click="open()">添加周报计划</t-button><t-button variant="outline" :loading="loading" @click="load">刷新</t-button></div>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <t-loading :loading="loading"><t-empty v-if="!loading&&!rows.length&&!error" description="尚未安排自动周报；仍可随时在会话中请求汇总或下载报告" /><div class="cards"><article v-for="row in rows" :key="row.id"><div class="row"><strong>{{ row.name }}</strong><t-tag :theme="row.enabled?'success':'default'">{{ row.enabled?'已启用':'已停用' }}</t-tag></div><p>{{ scopeName(row.scope_id) }} · {{ weekdays[row.weekday] }} {{ String(row.hour).padStart(2,'0') }}:{{ String(row.minute).padStart(2,'0') }}（北京时间）</p><p>发送到：{{ row.recipient_type==='source'?'原群／子区':`私聊 ${row.recipient_uid}` }} · {{ row.format.toUpperCase() }}</p><p v-if="row.next_run_at">下次：{{ time(row.next_run_at) }}</p><p v-if="row.last_run_at">上次：{{ time(row.last_run_at) }} · {{ statusLabel(row.last_status) }}</p><t-alert v-if="row.last_error" theme="warning">{{ row.last_error }}</t-alert><div class="row"><t-button variant="text" @click="open(row)">编辑</t-button><t-popconfirm content="删除后停止后续发送，已生成的问题记录保持不变。" @confirm="remove(row.id)"><t-button theme="danger" variant="text" :disabled="saving">删除</t-button></t-popconfirm></div></article></div></t-loading>
    <t-dialog v-model:visible="visible" :header="draft.id?'编辑周报计划':'添加周报计划'" :confirm-loading="saving" @confirm="save">
      <label>计划名称<t-input v-model="draft.name" :maxlength="128" placeholder="例如产品知识缺口周报" /></label>
      <label>发送 Bot 渠道<t-select v-model="draft.channel_id" :options="channels.map(c=>({value:c.id,label:`${c.name || c.bot_identity}${c.enabled?'':'（已停用）'}`}))" filterable placeholder="选择已配置的 Octo IM 渠道" @change="loadChannel" /></label>
      <label>统计来源<t-select v-model="draft.scope_id" :loading="channelLoading" :options="scopeOptions" filterable placeholder="选择该 Bot 的已绑定群／子区" /></label>
      <label>发送位置<t-radio-group v-model="draft.recipient_type"><t-radio-button value="source">原群／子区</t-radio-button><t-radio-button value="private">授权私聊</t-radio-button></t-radio-group></label>
      <label v-if="draft.recipient_type==='private'">接收人<t-select v-model="draft.recipient_uid" :options="dmUIDs.map(uid=>({value:uid,label:uid}))" filterable placeholder="仅列出渠道已授权私聊用户" /></label>
      <div class="row"><label>星期<t-select v-model="draft.weekday" :options="weekdays.map((label,value)=>({label,value}))" /></label><label>小时<t-input-number v-model="draft.hour" :min="0" :max="23" /></label><label>分钟<t-input-number v-model="draft.minute" :min="0" :max="59" /></label></div>
      <label>格式<t-select v-model="draft.format" :options="[{value:'text',label:'聊天文本'},{value:'markdown',label:'Markdown 文件'},{value:'html',label:'HTML 文件'},{value:'csv',label:'CSV 表格'}]" /></label>
      <label class="row"><t-switch v-model="draft.enabled" />启用自动发送</label><p>时间按北京时间（Asia/Shanghai）。接收人、区域或渠道权限失效后将停止发送并记录原因。</p><t-alert v-if="formError" theme="error">{{ formError }}</t-alert>
    </t-dialog>
  </section>
</template>
<script setup lang="ts">
import { computed,onBeforeUnmount,onMounted,ref,watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listAllIMChannels,listIMChannels,type IMChannelOverview } from '@/api/agent'
import { listReportSchedules,saveReportSchedule,deleteReportSchedule,type ReportSchedule } from '@/api/octo-business'
import type { OctoScope } from '@/api/octo'
import { useAuthStore } from '@/stores/auth'
const props=defineProps<{scopes:OctoScope[]}>(),auth=useAuthStore()
const rows=ref<ReportSchedule[]>([]),channels=ref<IMChannelOverview[]>([]),loading=ref(false),saving=ref(false),visible=ref(false),error=ref(''),formError=ref(''),channelLoading=ref(false),account=ref(''),dmUIDs=ref<string[]>([])
const weekdays=['周日','周一','周二','周三','周四','周五','周六'],blank=():ReportSchedule=>({id:'',name:'',channel_id:'',scope_id:'',recipient_type:'source',recipient_uid:'',weekday:1,hour:9,minute:0,timezone:'Asia/Shanghai',format:'text',enabled:false}),draft=ref(blank())
const scopeName=(id:string)=>props.scopes.find(s=>s.id===id)?.display_name||id,time=(value:string)=>new Date(value).toLocaleString(),statusLabel=(s?:string)=>({sent:'已发送',succeeded:'已发送',success:'已发送',failed:'失败',running:'发送中',queued:'等待发送'}[s||'']||s||'尚无结果')
const scopeOptions=computed(()=>props.scopes.filter(s=>s.account_id===account.value).map(s=>({label:`${s.subarea_id?'子区':'主群'} · ${s.display_name}`,value:s.id})))
let version=0,channelVersion=0
async function load(){const v=++version;loading.value=true;error.value='';try{const [r,c]=await Promise.all([listReportSchedules(),listAllIMChannels()]);if(v===version){rows.value=r.data;channels.value=(c.data||[]).filter(c=>c.platform==='octo')}}catch{if(v===version)error.value='周报计划读取失败，请检查服务状态和工作区权限。'}finally{if(v===version)loading.value=false}}
async function loadChannel(){const v=++channelVersion;account.value='';dmUIDs.value=[];formError.value='';const c=channels.value.find(c=>c.id===draft.value.channel_id);if(!c)return;channelLoading.value=true;try{const result=await listIMChannels(c.agent_id);if(v!==channelVersion)return;const channel=result.data.find(item=>item.id===c.id),cfg=channel?.public_config||{};account.value=String(cfg.account_id||'');dmUIDs.value=Array.isArray(cfg.allowed_dm_uids)?cfg.allowed_dm_uids:[]}catch{if(v===channelVersion)formError.value='渠道权限配置读取失败，请重试'}finally{if(v===channelVersion)channelLoading.value=false}}
async function open(row?:ReportSchedule){draft.value=row?{...row}:blank();visible.value=true;formError.value='';account.value='';dmUIDs.value=[];if(row)await loadChannel()}
async function save(){if(saving.value)return;if(!draft.value.name.trim()||!draft.value.channel_id||!draft.value.scope_id||(draft.value.recipient_type==='private'&&!draft.value.recipient_uid)){formError.value='请填写计划名称、渠道、来源及接收人';return}saving.value=true;formError.value='';try{await saveReportSchedule({...draft.value,recipient_uid:draft.value.recipient_type==='private'?draft.value.recipient_uid:''});visible.value=false;await load();await MessagePlugin.success('周报计划已保存')}catch{formError.value='保存失败，请确认渠道、来源绑定及接收人的私聊授权仍然有效'}finally{saving.value=false}}
async function remove(id:string){if(saving.value)return;saving.value=true;try{await deleteReportSchedule(id);await load()}catch{await MessagePlugin.error('删除计划失败，请重试')}finally{saving.value=false}}
watch(()=>auth.currentTenantId,()=>{version++;channelVersion++;rows.value=[];channels.value=[];visible.value=false;void load()});watch(visible,v=>{if(!v)channelVersion++});onMounted(load);onBeforeUnmount(()=>{version++;channelVersion++})
</script>
<style scoped>.toolbar,.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap}.toolbar{margin:20px 0}.toolbar>div{flex:1}.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:16px}article{border:1px solid var(--td-component-border);border-radius:8px;padding:18px}p{font-size:13px;color:var(--td-text-color-secondary);line-height:1.7}label{display:grid;gap:8px;margin:16px 0}.row>label{flex:1;min-width:100px}.row>label .t-input-number{width:100%}</style>
