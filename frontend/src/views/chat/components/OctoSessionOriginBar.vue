<template>
  <div v-if="origin && origin.platform === 'octo'" class="octo-origin-bar">
    <div class="origin-left">
      <span class="octo-brand-pill">
        <span class="octo-dot" />
        Octo 渠道
      </span>
      <span class="origin-type-pill" :class="`is-${origin.chat_type || 'group'}`">
        {{ chatTypeLabel }}
      </span>
      <span v-if="origin.source_name" class="origin-source" :title="origin.source_name">
        {{ origin.source_name }}
      </span>
      <span class="origin-sep" aria-hidden="true">/</span>
      <span v-if="displayUser" class="origin-user" :title="origin.user_uid ? `UID: ${origin.user_uid}` : ''">
        <span class="user-avatar">{{ userInitial }}</span>
        <b>{{ displayUser }}</b>
      </span>
      <span v-if="origin.channel_name" class="origin-bot" :title="`发送机器人：${origin.channel_name}`">
        Bot · {{ origin.channel_name }}
      </span>
    </div>
    <div v-if="origin.can_reply_octo" class="origin-route" :title="`回复将原路发往：${returnPathSummary}`">
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
  if (uid.length > 14) return `用户 ${uid.slice(0, 8)}…`
  return name || uid
})

const userInitial = computed(() => {
  const u = displayUser.value.trim()
  return u ? u.slice(0, 1).toUpperCase() : 'U'
})

const returnPathSummary = computed(() => {
  if (!props.origin) return ''
  if (props.origin.chat_type === 'direct') {
    return `私聊 · ${displayUser.value || '原用户'}`
  }
  const who = displayUser.value ? `@${displayUser.value} · ` : ''
  return `${who}${props.origin.source_name || chatTypeLabel.value}`
})
</script>

<style scoped lang="less">
.octo-origin-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding: 8px 18px;
  background: linear-gradient(90deg, var(--td-brand-color-light) 0%, var(--td-bg-color-container) 55%);
  border-bottom: 1px solid var(--td-component-border);
  font-size: 12.5px;
  color: var(--td-text-color-secondary);
}
.origin-left {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}
.octo-brand-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--td-brand-color);
  color: #fff;
  font-size: 11.5px;
  font-weight: 600;
}
.octo-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.9);
}
.origin-type-pill {
  padding: 2px 7px;
  border-radius: 6px;
  font-size: 11.5px;
  font-weight: 600;
  border: 1px solid var(--td-component-border);
  background: var(--td-bg-color-container);
  color: var(--td-text-color-primary);
  &.is-direct {
    border-color: var(--td-success-color-3);
    background: var(--td-success-color-1);
    color: var(--td-success-color);
  }
  &.is-subarea {
    border-color: var(--td-brand-color-3);
    background: var(--td-brand-color-light);
    color: var(--td-brand-color);
  }
}
.origin-source {
  font-weight: 600;
  color: var(--td-text-color-primary);
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.origin-sep {
  color: var(--td-component-border);
}
.origin-user {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  b {
    color: var(--td-text-color-primary);
    font-weight: 500;
  }
}
.user-avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--td-brand-color-2);
  color: var(--td-brand-color);
  font-size: 10.5px;
  font-weight: 700;
}
.origin-bot {
  padding: 1px 7px;
  border-radius: 5px;
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-placeholder);
  font-size: 11.5px;
}
.origin-route {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 10px;
  border-radius: 999px;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-brand-color-3);
  color: var(--td-brand-color);
  font-size: 12px;
  font-weight: 500;
  max-width: 380px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.route-dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--td-success-color);
}
</style>
