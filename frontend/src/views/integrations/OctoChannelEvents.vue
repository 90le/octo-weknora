<template>
  <t-drawer v-model:visible="visible" :header="`${channelName || 'Octo 渠道'} · 消息处理记录`" size="780px" :footer="false">
    <p>最近 100 条有效消息的处理状态。这里只显示投递信息，不显示聊天正文或密钥。</p><t-button variant="outline" :loading="loading" @click="load">刷新状态</t-button>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <t-table :data="rows" :columns="columns" :loading="loading" row-key="message_id" :empty="error?'无法读取状态，请重试':'尚无有效消息处理记录'">
      <template #state="{row}"><t-tag :theme="row.state==='failed'?'danger':row.state==='delivered'?'success':'default'">{{ states[row.state] || row.state }}</t-tag></template>
      <template #error_code="{row}">{{ errors[row.error_code] || row.error_code || '—' }}</template>
      <template #updated_at="{row}">{{ new Date(row.updated_at).toLocaleString() }}</template>
      <template #message_id="{row}"><small>{{ row.message_id }}</small></template>
    </t-table><p>“无需回复”是正常结束；投递重试只发送已生成的答案，不会再次执行知识操作。</p>
  </t-drawer>
</template>
<script setup lang="ts">
import { onBeforeUnmount,ref,watch } from 'vue'
import { channelEvents,type OctoChannelEvent } from '@/api/octo'
import { useAuthStore } from '@/stores/auth'
const visible=defineModel<boolean>('visible',{default:false}),props=defineProps<{channelId:string;channelName:string}>(),auth=useAuthStore()
const rows=ref<OctoChannelEvent[]>([]),loading=ref(false),error=ref('');let version=0
const states:Record<string,string>={queued:'等待处理',processing:'处理中',reply_pending:'等待重试投递',delivered:'已回复',finished:'已结束（无需回复）',ignored:'权限变更，未发送',failed:'处理失败'}
const errors:Record<string,string>={dispatch_failed:'消息未能进入处理队列',send_failed:'回复发送失败',delivery_not_acknowledged:'平台未确认收到回复',authorization_changed:'权限已变更',execution_interrupted:'处理被中断',delivery_retry_exhausted:'已达到投递重试上限',invalid_stored_input:'消息记录无法恢复'}
const columns=[{colKey:'state',title:'状态',width:160},{colKey:'attempts',title:'投递次数',width:90},{colKey:'error_code',title:'说明',minWidth:200},{colKey:'updated_at',title:'最后更新',minWidth:180},{colKey:'message_id',title:'消息编号',minWidth:230}]
async function load(){const v=++version;if(!props.channelId)return;loading.value=true;error.value='';rows.value=[];try{const result=await channelEvents(props.channelId);if(v===version)rows.value=result.data}catch{if(v===version)error.value='消息状态读取失败，请检查当前工作区权限。'}finally{if(v===version)loading.value=false}}
watch(()=>[visible.value,props.channelId],()=>{version++;if(visible.value&&props.channelId)void load();else rows.value=[]})
watch(()=>auth.currentTenantId,()=>{version++;rows.value=[];visible.value=false});onBeforeUnmount(()=>{version++})
</script>
<style scoped>p{font-size:13px;color:var(--td-text-color-secondary);line-height:1.65}.t-table{margin-top:16px}small{overflow-wrap:anywhere}</style>
