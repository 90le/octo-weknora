<template>
  <section class="channel-chat" :aria-label="t('title')">
    <header class="channel-chat__header">
      <router-link class="channel-chat__back" :to="{ name: 'artifactLibrary' }" :aria-label="$t('artifactLibrary.title')">
        <t-icon name="chevron-left" size="20px" />
      </router-link>
      <h1>{{ t('title') }}</h1>
    </header>
    <p class="channel-chat__scope">{{ t('scope') }}</p>
    <div ref="scrollRoot" class="channel-chat__scroll">
      <div class="channel-chat__thread">
        <p v-if="error" role="alert">{{ t('error') }}</p>
        <p v-else-if="loading" role="status">{{ t('loading') }}</p>
        <ol v-else class="channel-chat__messages">
          <li v-for="message in messages" :key="message.id" :data-message-id="message.id"
            :aria-current="message.id === messageId ? 'true' : undefined"
            class="channel-chat__message" :class="{ 'channel-chat__message--user': message.role === 'user' }">
            <div class="channel-chat__meta"><strong>{{ t(message.role === 'user' ? 'user' : 'assistant') }}</strong><time>{{ message.created_at }}</time></div>
            <div class="channel-chat__content">{{ message.content }}</div>
          </li>
        </ol>
      </div>
    </div>
    <nav class="channel-chat__pagination" :aria-label="t('title')">
      <button type="button" :disabled="loading || page <= 1" @click="load(page - 1)">{{ t('previous') }}</button>
      <span>{{ t('page', { page }) }}</span>
      <button type="button" :disabled="loading || !hasMore" @click="load(page + 1)">{{ t('next') }}</button>
    </nav>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { get } from '@/utils/request'
import { channelConversationMessages } from './channelConversationI18n'

const route = useRoute()
const { t } = useI18n({ useScope: 'local', messages: channelConversationMessages })
const scrollRoot = ref<HTMLElement | null>(null)
const messageId = computed(() => typeof route.params.messageId === 'string' ? route.params.messageId : '')
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const error = ref(false)
const messages = ref<Array<{ id: string; role: string; content: string; created_at: string }>>([])
let requestId = 0
async function load(next?: number) {
  const ticket = ++requestId
  const { sessionId, messageId, index } = route.params
  messages.value = []
  hasMore.value = false
  error.value = false
  loading.value = true
  try {
    if (typeof sessionId !== 'string' || typeof messageId !== 'string' || typeof index !== 'string' || !/^\d+$/.test(index)) throw new Error('Invalid conversation')
    const url = `/api/v1/artifacts/channels/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(messageId)}/${encodeURIComponent(index)}/conversation`
    const result = await get<{ success: boolean; data: typeof messages.value; page: number; has_more: boolean }>(url, {
      params: next === undefined ? {} : { page: next },
    })
    if (ticket !== requestId) return
    if (!result.success) throw new Error('Conversation unavailable')
    messages.value = result.data
    page.value = result.page
    hasMore.value = result.has_more
    loading.value = false
    if (next === undefined) {
      await nextTick()
      if (ticket === requestId) scrollRoot.value?.querySelector('li[aria-current="true"]')?.scrollIntoView({ block: 'center' })
    } else {
      scrollRoot.value?.scrollTo({ top: 0 })
    }
  } catch {
    if (ticket !== requestId) return
    messages.value = []
    hasMore.value = false
    error.value = true
  } finally {
    if (ticket === requestId) loading.value = false
  }
}
watch(() => [route.params.sessionId, route.params.messageId, route.params.index], () => {
  page.value = 1
  void load()
}, { immediate: true })
onBeforeUnmount(() => { requestId++ })
</script>

<style scoped>
.channel-chat { height: 100%; min-height: 0; display: flex; flex-direction: column; background: var(--td-bg-color-container); color: var(--td-text-color-primary); }
.channel-chat__header { min-height: 56px; display: flex; align-items: center; gap: 12px; padding: 0 24px; border-bottom: 1px solid var(--td-component-stroke); }
.channel-chat__header h1 { margin: 0; font-size: var(--app-text-xl); font-weight: 600; }
.channel-chat__back { display: inline-flex; color: inherit; border-radius: var(--app-radius-xs); }
.channel-chat__back:focus-visible { outline: 2px solid var(--td-brand-color); }
.channel-chat__scope { margin: 0; padding: 10px 24px; color: var(--td-text-color-secondary); font-size: var(--app-text-md); border-bottom: 1px solid var(--td-component-stroke); }
.channel-chat__scroll { flex: 1; min-height: 0; overflow: auto; }
.channel-chat__thread { max-width: 800px; margin: 0 auto; padding: 28px 24px; }
.channel-chat__messages { display: flex; flex-direction: column; gap: 28px; margin: 0; padding: 0; list-style: none; }
.channel-chat__message { max-width: 100%; align-self: stretch; border-radius: var(--app-radius-xl); }
.channel-chat__message--user { align-self: flex-end; max-width: 85%; }
.channel-chat__meta { display: flex; gap: 12px; margin-bottom: 8px; color: var(--td-text-color-secondary); font-size: var(--app-text-sm); }
.channel-chat__message--user .channel-chat__meta { justify-content: flex-end; }
.channel-chat__content { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.7; }
.channel-chat__message--user .channel-chat__content { padding: 12px 16px; border-radius: var(--app-radius-xl); background: var(--td-bg-color-secondarycontainer); }
.channel-chat__message[aria-current] { outline: 2px solid var(--td-brand-color); outline-offset: 8px; }
.channel-chat__pagination { display: flex; align-items: center; justify-content: center; gap: 16px; padding: 12px; border-top: 1px solid var(--td-component-stroke); color: var(--td-text-color-secondary); }
.channel-chat__pagination button { padding: 5px 12px; color: inherit; background: transparent; border: 1px solid var(--td-component-stroke); border-radius: var(--app-radius-sm); cursor: pointer; }
.channel-chat__pagination button:disabled { opacity: .45; cursor: default; }
</style>
