<template>
  <t-dialog v-model:visible="visible" header="连接 Octo Bot" attach="body" :z-index="2600" :confirm-loading="saving" :confirm-btn="{ content: identity ? '保存已核验的连接' : '核验 Bot 身份', disabled: !token.trim() || !account.trim() }" @confirm="confirm" @closed="reset">
    <p class="hint">使用 Bot Token 连接。身份由 Octo 返回，保存后可在 IM 渠道中选择，不需要填写 Bot UID。</p>
    <label>连接名称<t-input v-model="account" :maxlength="128" autocomplete="off" placeholder="例如 octo-xiaoqiu" :disabled="saving" /></label>
    <p class="hint">这是本工作区内的唯一标识。使用已有名称会更新该连接凭据。</p>
    <label>Bot Token<t-input v-model="token" type="password" autocomplete="new-password" :disabled="saving" placeholder="bf_…" /></label>
    <t-alert v-if="identity" theme="success">已核验：{{ identity.name || 'Octo Bot' }} · {{ identity.bot_uid }}</t-alert>
    <t-alert v-if="error" theme="error">{{ error }}</t-alert>
    <p class="hint">密钥加密保存，不回显；核验身份不会启动消息接收。启用渠道前，应停止此 Bot 的其他接收端。</p>
  </t-dialog>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { probeConnection, saveConnection, type OctoIdentity } from '@/api/octo'
const visible = defineModel<boolean>('visible', { default: false })
const emit = defineEmits<{ saved: [account: string, identity: OctoIdentity] }>()
const account = ref(''), token = ref(''), error = ref(''), saving = ref(false), identity = ref<OctoIdentity | null>(null)
let version = 0
watch(token, () => { version++; identity.value = null; error.value = '' })
function reset() { version++; account.value = ''; token.value = ''; identity.value = null; error.value = '' }
async function confirm() {
  if (saving.value || !account.value.trim() || !token.value.trim()) return
  saving.value = true; error.value = ''
  const requestVersion = version, connection = account.value.trim()
  try {
    if (!identity.value) {
      const result = await probeConnection(token.value.trim())
      if (version === requestVersion && visible.value) identity.value = result.data
    } else {
      const result = await saveConnection(connection, token.value.trim(), identity.value.bot_uid)
      if (version !== requestVersion || !visible.value) return
      emit('saved', connection, result.data)
      visible.value = false; reset(); await MessagePlugin.success('Octo 连接已加密保存')
    }
  } catch { if (version === requestVersion) error.value = '未能核验或保存连接。请检查 Token 是否有效及服务网络；原有凭据未被替换。' }
  finally { saving.value = false }
}
</script>
<style scoped>label { display:grid; gap:8px; margin:18px 0 8px; }.hint { color:var(--td-text-color-secondary); line-height:1.65; font-size:13px; }</style>
