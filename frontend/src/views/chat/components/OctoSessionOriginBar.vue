<template>
  <div v-if="origin && origin.platform === 'octo'" class="octo-origin-bar">
    <div class="origin-main">
      <t-tag size="small" theme="primary" variant="light">Octo 渠道</t-tag>
      <t-tag size="small" variant="outline">{{ chatTypeLabel }}</t-tag>
      <span v-if="origin.channel_name" class="origin-item">
        <span class="origin-k">Bot</span>
        <b>{{ origin.channel_name }}</b>
      </span>
      <span v-if="origin.source_name" class="origin-item">
        <span class="origin-k">来源</span>
        <b>{{ origin.source_name }}</b>
      </span>
      <span v-if="displayUser" class="origin-item" :title="origin.user_uid ? `UID: ${origin.user_uid}` : ''">
        <span class="origin-k">成员</span>
        <b>{{ displayUser }}</b>
      </span>
    </div>
    <div v-if="origin.can_reply_octo" class="origin-route" :title="`回传目标：${returnPathSummary}`">
      <span class="route-dot" />
      <span>原路返回：{{ returnPathSummary }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { SessionIMOrigin } from '@/api/chat'

const props = defineProps<{ origin?: SessionIMOrigin | null }>()

const chatTypeLabel = computed(() => {
  const t = props.origin?.chat_type
  if (t === 'direct') return '私聊'
  if (t === 'subarea') return '子区'
  if (t === 'group') return '主群'
  return '会话'
})

const displayUser = computed(() => {
  const name = (props.origin?.user_name || '').trim()
  const uid = (props.origin?.user_uid || '').trim()
  if (name && name !== uid) return name
  if (uid.length > 14) return `${uid.slice(0, 8)}…`
  return name || uid
})

const returnPathSummary = computed(() => {
  if (!props.origin) return ''
  if (props.origin.chat_type === 'direct') {
    return `私聊 · ${displayUser.value || '原用户'}`
  }
  return `${chatTypeLabel.value} · ${props.origin.source_name || props.origin.group_id || '原群会话'}`
})
</script>

<style scoped lang="less">
.octo-origin-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  padding: 6px 16px;
  background: var(--td-bg-color-secondarycontainer);
  border-bottom: 1px solid var(--td-component-border);
  font-size: 12px;
  color: var(--td-text-color-secondary);
}
.origin-main {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.origin-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.origin-item b {
  color: var(--td-text-color-primary);
  font-weight: 500;
}
.origin-k {
  color: var(--td-text-color-placeholder);
}
.origin-route {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--td-brand-color);
  font-weight: 500;
}
.route-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--td-success-color);
}
</style>
