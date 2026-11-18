<template>
  <t-drawer v-model:visible="visible" :header="t(key + 'title')" size="min(640px, 100vw)" :footer="false">
    <div class="generation-tools">
      <t-button theme="primary" :disabled="!retryable.length" :loading="saving"
        @click="retry(retryable)">
        {{ t(key + 'retryBatch', { count: retryable.length }) }}
      </t-button>
      <t-button variant="outline" :disabled="saving || loading" @click="load()">
        {{ t(key + 'refresh') }}
      </t-button>
    </div>
    <p class="generation-description">{{ t(key + 'scope') }}</p>
    <p v-if="error" role="alert" class="generation-error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <t-loading :loading="loading && !tasks.length">
      <p v-if="!loading && !error && !tasks.length">{{ t(key + 'empty') }}</p>
      <ul v-else class="generation-list">
        <li v-for="task in tasks" :key="task.knowledge_id" class="generation-row">
          <div class="generation-doc">
            <strong>{{ task.title || task.knowledge_id }}</strong>
            <div v-if="task.error_code" class="generation-description">{{ t(key + task.error_code) }}</div>
            <div v-if="task.status === 'failed' && task.failed_at" class="generation-description">
              {{ formatTime(task.failed_at) }}
            </div>
            <div v-if="task.attempts > 0 && (task.status === 'queued' || task.status === 'running')"
              class="generation-description">{{ t(key + 'attempted', { count: task.attempts }) }}</div>
          </div>
          <t-tag :theme="task.status === 'failed' ? 'danger' : task.status === 'published' ? 'success' : 'default'"
            variant="light">{{ t(key + task.status) }}</t-tag>
          <t-button v-if="task.status === 'failed'" size="small" variant="text" theme="primary"
            :disabled="saving" @click="retry([task.knowledge_id])">{{ t(key + 'retry') }}</t-button>
        </li>
      </ul>
    </t-loading>
    <div v-if="history.length || nextCursor" class="generation-tools generation-pagination">
      <t-button variant="outline" :disabled="!history.length || loading || saving" @click="previous">
        {{ t(key + 'previous') }}
      </t-button>
      <t-button variant="outline" :disabled="!nextCursor || loading || saving" @click="next">
        {{ t(key + 'next') }}
      </t-button>
    </div>
  </t-drawer>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { listWikiGenerationTasks, retryWikiGenerationTasks, type WikiGenerationTask } from '@/api/wiki'

const props = defineProps<{ kbId: string }>()
const visible = defineModel<boolean>('visible', { required: true })
const emit = defineEmits<{ queued: []; published: [] }>()
const { t, locale } = useI18n()
const key = 'knowledgeEditor.wikiBrowser.generation.'
const tasks = ref<WikiGenerationTask[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const cursor = ref('')
const nextCursor = ref('')
const history = ref<string[]>([])
const retryable = computed(() => tasks.value.filter(task => task.status === 'failed')
  .slice(0, 20).map(task => task.knowledge_id))

let revision = 0
let timer: ReturnType<typeof setTimeout> | undefined

function formatTime(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value))
}

function schedulePoll() {
  clearTimeout(timer)
  if (visible.value && tasks.value.some(task => task.status === 'queued' || task.status === 'running')) {
    timer = setTimeout(() => void load(), 5000)
  }
}

async function load(target = cursor.value): Promise<boolean> {
  const request = ++revision
  loading.value = true
  try {
    const { data } = await listWikiGenerationTasks(props.kbId, target)
    if (request !== revision) return false
    const active = new Set(tasks.value.filter(task => task.status === 'queued' || task.status === 'running')
      .map(task => task.knowledge_id))
    tasks.value = data.tasks
    nextCursor.value = data.next_cursor || ''
    cursor.value = target
    if (tasks.value.some(task => task.status === 'published' && active.has(task.knowledge_id))) emit('published')
    return true
  } catch {
    if (request === revision) error.value = t(key + 'loadError')
    return false
  } finally {
    if (request === revision) {
      loading.value = false
      schedulePoll()
    }
  }
}

async function retry(ids: string[]) {
  if (saving.value || !ids.length) return
  saving.value = true
  clearTimeout(timer)
  revision++
  error.value = ''
  notice.value = ''
  try {
    const { data } = await retryWikiGenerationTasks(props.kbId, ids)
    if (!visible.value) return
    notice.value = t(key + 'queuedNotice', { count: data.queued.length, skipped: data.skipped })
    if (data.dispatch_pending) notice.value += ' ' + t(key + 'dispatchPending')
    if (data.queued.length) emit('queued')
    await load()
  } catch (cause) {
    if (!visible.value) return
    const status = (cause as { status?: number })?.status
    error.value = t(key + (status === 503 ? 'modelError' : 'retryError'))
    schedulePoll()
  } finally {
    loading.value = false
    saving.value = false
  }
}

async function previous() {
  const target = history.value.at(-1)
  if (target !== undefined && await load(target)) history.value.pop()
}

async function next() {
  const current = cursor.value
  if (nextCursor.value && await load(nextCursor.value)) history.value.push(current)
}

watch(visible, (open) => {
  if (open) {
    error.value = ''
    notice.value = ''
    void load()
  } else {
    revision++
    clearTimeout(timer)
    loading.value = false
  }
})
onUnmounted(() => { revision++; clearTimeout(timer) })
</script>

<style scoped>
.generation-tools { display: flex; flex-wrap: wrap; gap: 8px; }
.generation-description { color: var(--td-text-color-secondary); font-size: 13px; }
.generation-error { color: var(--td-error-color); }
.generation-list { list-style: none; padding: 0; margin: 12px 0; }
.generation-row {
  display: flex;
  align-items: center;
  min-height: 48px;
  gap: 8px;
  padding: 8px 0;
  border-bottom: 1px solid var(--td-component-stroke);
}
.generation-doc { min-width: 0; flex: 1; overflow-wrap: anywhere; }
.generation-pagination { justify-content: flex-end; }
</style>
