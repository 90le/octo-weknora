<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  applyGitHubScheduleMigration,
  previewGitHubScheduleMigration,
  type GitHubScheduleMigrationApplyResponse,
  type GitHubScheduleMigrationPreviewItem,
  type GitHubScheduleMigrationPreviewResponse,
} from '@/api/datasource'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import { selectedGitHubScheduleMigrations } from './githubScheduleMigrationState'

const props = defineProps<{ visible: boolean; kbId: string }>()
const emit = defineEmits<{
  (event: 'update:visible', value: boolean): void
  (event: 'applied'): void
}>()
const { t } = useI18n()
const loading = ref(false)
const applying = ref(false)
const error = ref('')
const preview = ref<GitHubScheduleMigrationPreviewResponse | null>(null)
const selectedIDs = ref<string[]>([])
const result = ref<GitHubScheduleMigrationApplyResponse | null>(null)
let generation = 0

const eligible = computed(() => preview.value?.items.filter(item => item.eligible) || [])
const selected = computed(() => eligible.value.filter(item => selectedIDs.value.includes(item.data_source_id)))
const canApply = computed(() => !loading.value && !applying.value && selected.value.length > 0)
const appliedCount = computed(() => result.value?.results.filter(item => item.status === 'applied').length || 0)

function unwrap<T>(response: unknown): T {
  const wrapped = response as { data?: unknown } | undefined
  return (wrapped?.data ?? response) as T
}

function onVisibleChange(value: boolean) {
  if (!value && applying.value) return
  emit('update:visible', value)
}

async function loadPreview() {
  const current = ++generation
  loading.value = true
  error.value = ''
  selectedIDs.value = []
  try {
    const data = unwrap<GitHubScheduleMigrationPreviewResponse>(await previewGitHubScheduleMigration(props.kbId))
    if (current !== generation || !props.visible) return
    preview.value = data
  } catch {
    if (current === generation) error.value = t('datasource.githubBulk.migration.previewFailed')
  } finally {
    if (current === generation) loading.value = false
  }
}

watch(() => props.visible, (opened) => {
  if (opened) {
    preview.value = null
    result.value = null
    void loadPreview()
  } else {
    generation += 1
  }
})

function reasonCode(code: string): string {
  const key = `datasource.githubBulk.migration.reason.${code}`
  const translated = t(key)
  return translated === key ? t('datasource.githubBulk.migration.reason.other') : translated
}

function reason(item: GitHubScheduleMigrationPreviewItem): string {
  return reasonCode(item.reason)
}

function selectAll() {
  selectedIDs.value = eligible.value.map(item => item.data_source_id)
}

async function applySelected() {
  if (!canApply.value) return
  const selections = selectedGitHubScheduleMigrations(preview.value?.items || [], selectedIDs.value)
  applying.value = true
  error.value = ''
  try {
    result.value = unwrap<GitHubScheduleMigrationApplyResponse>(await applyGitHubScheduleMigration(props.kbId, selections))
    selectedIDs.value = []
    emit('applied')
    if (result.value.results.some(item => item.status === 'skipped' || item.reason)) {
      MessagePlugin.warning(t('datasource.githubBulk.migration.partial'))
    } else {
      MessagePlugin.success(t('datasource.githubBulk.migration.applied', { count: appliedCount.value }))
    }
    await loadPreview()
  } catch {
    error.value = t('datasource.githubBulk.migration.applyFailed')
  } finally {
    applying.value = false
  }
}
</script>

<template>
  <SettingDrawer
    :visible="visible"
    :title="t('datasource.githubBulk.migration.title')"
    :description="t('datasource.githubBulk.migration.description')"
    icon="time"
    width="680px"
    :confirm-text="t('datasource.githubBulk.migration.applyCount', { count: selected.length })"
    :confirm-loading="applying"
    :confirm-disabled="!canApply"
    @update:visible="onVisibleChange"
    @cancel="onVisibleChange(false)"
    @confirm="applySelected"
  >
    <t-alert theme="info" :message="t('datasource.githubBulk.migration.scope')" />
    <t-loading :loading="loading" size="small">
      <p v-if="error" class="migration-error">{{ error }}</p>
      <template v-if="preview">
        <div class="migration-actions">
          <span>{{ t('datasource.githubBulk.migration.eligibleCount', { count: eligible.length }) }}</span>
          <t-button v-if="eligible.length" variant="text" size="small" @click="selectAll">
            {{ t('datasource.githubBulk.migration.selectAll') }}
          </t-button>
        </div>
        <div v-if="preview.items.length" class="migration-list">
          <t-checkbox-group v-model="selectedIDs">
            <div v-for="item in preview.items" :key="item.data_source_id" class="migration-row">
              <t-checkbox :value="item.data_source_id" :disabled="!item.eligible" />
              <div class="migration-row__body">
                <strong>{{ item.repository || item.data_source_id }}</strong>
                <span>{{ item.mode === 'source' ? t('datasource.source.readonly') : t('datasource.source.documents') }}</span>
                <code>{{ item.current_schedule || '—' }} → {{ item.proposed_schedule || '—' }}</code>
                <small v-if="!item.eligible">{{ reason(item) }}</small>
              </div>
            </div>
          </t-checkbox-group>
        </div>
        <t-empty v-else :description="t('datasource.githubBulk.migration.empty')" />
      </template>
      <div v-if="result" class="migration-result">
        <strong>{{ t('datasource.githubBulk.migration.applied', { count: appliedCount }) }}</strong>
        <p v-for="item in result.results.filter(row => row.status === 'skipped')" :key="item.data_source_id">
          {{ item.data_source_id }} · {{ t('datasource.githubBulk.migration.skipped') }} · {{ reasonCode(item.reason) }}
        </p>
      </div>
    </t-loading>
  </SettingDrawer>
</template>

<style scoped lang="less">
.migration-actions { display: flex; justify-content: space-between; align-items: center; margin: 16px 0 8px; color: var(--td-text-color-secondary); font-size: 13px; }
.migration-list { max-height: min(55vh, 520px); overflow: auto; border: 1px solid var(--td-component-stroke); border-radius: 8px; }
.migration-row { display: flex; gap: 10px; align-items: flex-start; padding: 10px 12px; border-bottom: 1px solid var(--td-component-stroke); }
.migration-row:last-child { border-bottom: 0; }
.migration-row__body { display: flex; min-width: 0; flex-direction: column; gap: 3px; font-size: 12px; }
.migration-row__body strong { color: var(--td-text-color-primary); overflow-wrap: anywhere; }
.migration-row__body code { overflow-wrap: anywhere; }
.migration-row__body small { color: var(--td-text-color-placeholder); }
.migration-error { color: var(--td-error-color); }
.migration-result { margin-top: 14px; font-size: 12px; }
.migration-result p { margin: 5px 0; overflow-wrap: anywhere; }
</style>
