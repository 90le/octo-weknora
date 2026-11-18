<template>
  <section class="reply-box">
    <h4>原渠道人工回复</h4>
    <template v-if="!issue.archive_state">
      <t-select v-model="target" :options="targets" :disabled="busy" aria-label="回复对象" />
      <t-textarea v-model="body" :maxlength="750" :disabled="busy" placeholder="输入要通过原 Bot 发到原会话的回复" />
      <t-button variant="outline" :disabled="!body.trim()" :loading="busy" @click="prepare">预览回复</t-button>
    </template>
    <t-alert v-if="error" theme="warning">{{ error }}</t-alert>
    <details v-if="history.length">
      <summary>发送记录（{{ history.length }}）</summary>
      <ul>
        <li v-for="row in history" :key="row.id">
          <div>{{ new Date(row.created_at).toLocaleString() }} · {{ row.user_name || row.user_uid }} · {{ phases[row.status] || row.status }}</div>
          <small v-if="row.receipt">Octo 消息 {{ row.receipt }}</small>
          <OctoMarkdown :text="row.body" compact />
        </li>
      </ul>
    </details>
    <t-dialog v-model:visible="visible" attach="body" header="确认原渠道回复"
      :confirm-btn="{content:'确认发送',loading:busy}" @confirm="send">
      <template v-if="preview">
        <dl>
          <dt>Bot 渠道</dt><dd>{{ botName || preview.channel_id }}</dd>
          <dt>会话</dt><dd>{{ preview.scope_name || issue.scope_name }}</dd>
          <dt>引用消息</dt><dd>{{ preview.message_id }}</dd>
          <dt>接收者</dt><dd>{{ preview.user_name || preview.user_uid }}</dd>
        </dl>
        <OctoMarkdown :text="preview.body" />
      </template>
    </t-dialog>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { listAllIMChannels } from '@/api/agent'
import { issueReplies, previewIssueReply, sendIssueReply, type IssueReply, type OctoIssue } from '@/api/octo-business'
import OctoMarkdown from './OctoMarkdown.vue'

const props = defineProps<{issue:OctoIssue}>()
const targets = [{value:'question',label:'回复原始提问者'},{value:'feedback',label:'回复反馈提交者'}]
const phases:Record<string,string> = {sent:'已发送',sending:'发送中',blocked:'当前权限已变更',uncertain:'送达未确认'}
const target = ref('question')
const body = ref('')
const busy = ref(false)
const error = ref('')
const visible = ref(false)
const preview = ref<IssueReply>()
const history = ref<IssueReply[]>([])
const botName = ref('')
async function load() {
  try { history.value = (await issueReplies(props.issue.id)).data }
  catch { error.value ||= '发送记录读取失败。' }
}
async function prepare() {
  if (busy.value || !body.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    const response = await previewIssueReply(props.issue.id,target.value,body.value)
    preview.value = response.data
    try {
      const channels = await listAllIMChannels()
      const channel = channels.data.find(item => item.id === response.data.channel_id)
      botName.value = channel?.name || channel?.bot_identity || ''
    } catch { botName.value = '' }
    visible.value = true
  } catch { error.value = '找不到当前有权限使用的原始消息。可以改选反馈提交者，或保存内部处理说明。' }
  finally { busy.value = false }
}
async function send() {
  if (!preview.value || busy.value) return
  busy.value = true
  try {
    const response = await sendIssueReply(props.issue.id,preview.value.id)
    visible.value = false
    if (response.data.status === 'sent') body.value = ''
    else error.value = (phases[response.data.status] || '发送未完成') + '。请先核对 Octo 原会话。'
    await load()
  } catch { error.value = '发送结果未确认。请先核对发送记录和原会话。'; visible.value = false; await load() }
  finally { busy.value = false }
}
onMounted(load)
</script>
<style scoped>
.reply-box { display:grid; gap:12px; border-top:1px solid var(--td-component-border); padding-block:16px; }
.reply-box h4 { margin:0; }
.reply-box :deep(.t-button) { justify-self:start; }
dl { display:grid; grid-template-columns:auto 1fr; gap:8px 12px; }
dd { margin:0; overflow-wrap:anywhere; }
li + li { margin-top:12px; }
</style>
