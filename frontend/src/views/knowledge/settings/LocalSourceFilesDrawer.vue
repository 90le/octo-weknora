<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getLocalSourceFiles, type DataSource, type LocalSourceFilePage, type LocalSourceFileState } from '@/api/datasource'

const props = defineProps<{ source: DataSource | null; canManage: boolean }>()
const visible = defineModel<boolean>('visible', { default: false })
const emit = defineEmits<{ (event: 'sync', source: DataSource): void }>()
const { t } = useI18n()
const statuses = ['indexed', 'processing', 'failed', 'ignored', 'missing_retained', 'pending_delete', 'removed'] as const
const filter = ref('')
const page = ref<LocalSourceFilePage | null>(null)
const items = ref<LocalSourceFileState[]>([])
const loading = ref(false)
const error = ref('')
let generation = 0
async function load(reset = false) {
  const source = props.source
  if (!source || loading.value) return
  const current = generation
  const offset = reset ? 0 : items.value.length
  loading.value = true; error.value = ''
  if (reset) { items.value = []; page.value = null }
  try {
    const response: any = await getLocalSourceFiles(source.id, filter.value, offset)
    const next = (response?.data ?? response) as LocalSourceFilePage
    if (current !== generation || source.id !== props.source?.id) return
    items.value = reset ? next.items : [...items.value, ...next.items]
    page.value = next
  } catch (e: any) { if (current === generation) error.value = e?.message || t('storageSpaces.fileStatusUnavailable') }
  finally { if (current === generation) loading.value = false }
}
watch(() => [visible.value, props.source?.id] as const, ([open]) => {
  ++generation; loading.value = false; filter.value = ''; items.value = []; page.value = null; error.value = ''
  if (open && props.source) void load(true)
})
function selectFilter(value: string) {
  if (value === filter.value) return
  ++generation; loading.value = false; filter.value = value; void load(true)
}
function statusText(value: string) { return t(`storageSpaces.fileStatuses.${value}`) }
function reasonText(value?: string) {
  if (!value) return ''
  const key = `storageSpaces.fileReasons.${value}`
  const message = t(key)
  return message === key ? value : message
}
</script>

<template>
  <t-drawer v-model:visible="visible" size="680px" destroy-on-close>
    <template #header>{{ t('storageSpaces.fileStatusTitle') }} · {{ source?.name }}</template>
    <p class="hint">{{ t('storageSpaces.fileStatusHint') }}</p>
    <div class="toolbar">
      <t-button variant="outline" :loading="loading" @click="load(true)">{{ t('sourceRoots.refresh') }}</t-button>
      <t-button v-if="canManage && source" variant="outline" @click="emit('sync', source)">{{ t('storageSpaces.retrySync') }}</t-button>
    </div>
    <div v-if="page" class="filters">
      <t-button size="small" :variant="filter === '' ? 'base' : 'outline'" @click="selectFilter('')">{{ t('storageSpaces.fileAll') }}</t-button>
      <t-button v-for="status in statuses" :key="status" size="small" :variant="filter === status ? 'base' : 'outline'" @click="selectFilter(status)">{{ statusText(status) }} {{ page.counts[status] || 0 }}</t-button>
    </div>
    <t-alert v-if="error" theme="warning" :message="error" />
    <t-loading :loading="loading">
      <p v-if="!items.length && !loading && !error" class="hint">{{ t('storageSpaces.fileStatusEmpty') }}</p>
      <div v-for="item in items" :key="item.path" class="file-row">
        <div><strong>{{ item.path }}</strong><p class="hint">{{ reasonText(item.reason) }} <span v-if="item.status === 'failed' && item.has_previous">{{ t('storageSpaces.previousPreserved') }}</span></p><small class="hint">{{ new Date(item.observed_at).toLocaleString() }}</small></div>
        <t-tag :theme="item.status === 'failed' || item.status === 'pending_delete' ? 'warning' : item.status === 'indexed' ? 'success' : 'default'" variant="light">{{ statusText(item.status) }}</t-tag>
      </div>
      <t-button v-if="page?.has_more" variant="outline" :loading="loading" @click="load(false)">{{ t('storageSpaces.reviewLoadMore') }}</t-button>
    </t-loading>
  </t-drawer>
</template>

<style scoped>
.hint{font-size:var(--app-text-sm);line-height:1.6;color:var(--td-text-color-secondary)}.toolbar,.filters{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0}.file-row{display:flex;justify-content:space-between;gap:16px;padding:14px 0;border-bottom:1px solid var(--td-component-border)}.file-row strong{overflow-wrap:anywhere}.file-row p{margin:4px 0}.file-row>div{min-width:0}
</style>
