<template>
  <section ref="root" :aria-label="t('title')">
    <p>{{ t('scope') }}</p>
    <p v-if="error" role="alert">{{ t('error') }}</p>
    <p v-if="loading" role="status">{{ t('loading') }}</p>
    <ol v-else>
      <li v-for="message in messages" :key="message.id" :data-message-id="message.id" :aria-current="message.id === item.message_id ? 'true' : undefined">
        <strong>{{ t(message.role === 'user' ? 'user' : 'assistant') }}</strong>
        <time>{{ message.created_at }}</time>
        <pre>{{ message.content }}</pre>
      </li>
    </ol>
    <button type="button" :disabled="loading || page <= 1" @click="load(page - 1)">{{ t('previous') }}</button>
    <span>{{ t('page', { page }) }}</span>
    <button type="button" :disabled="loading || !hasMore" @click="load(page + 1)">{{ t('next') }}</button>
  </section>
</template>

<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { get } from '@/utils/request'
import type { ArtifactLibraryItem } from '@/api/artifacts'
import { channelConversationMessages } from './channelConversationI18n'

const props = defineProps<{ item: ArtifactLibraryItem }>()
const root = ref<HTMLElement | null>(null)
const { t } = useI18n({ useScope: 'local', messages: channelConversationMessages })
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const error = ref(false)
const messages = ref<Array<{ id: string; role: string; content: string; created_at: string }>>([])
async function load(next?: number) {
  loading.value = true
  error.value = false
  try {
    const item = props.item
    const url = `/api/v1/artifacts/channels/${encodeURIComponent(item.session_id)}/messages/${encodeURIComponent(item.message_id)}/${item.index}/conversation`
    const result = await get<{ success: boolean; data: typeof messages.value; page: number; has_more: boolean }>(url, {
      params: next === undefined ? {} : { page: next },
    })
    messages.value = result.data
    page.value = result.page
    hasMore.value = result.has_more
    if (next === undefined) {
      await nextTick()
      root.value?.querySelector('li[aria-current="true"]')?.scrollIntoView({ block: 'center' })
    }
  } catch {
    messages.value = []
    hasMore.value = false
    error.value = true
  } finally { loading.value = false }
}
onMounted(() => load())
</script>

<style scoped>
pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; }
li[aria-current] { outline: 2px solid currentColor; }
time { margin-left: 1em; }
</style>
