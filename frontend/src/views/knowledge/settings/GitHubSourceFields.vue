<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { previewGitHubDocumentScope, type GitHubDocumentScopePreview } from '@/api/datasource'
import { canShowGitHubScopePreview, githubScopePreviewErrorKey, githubScopePreviewRequest } from './githubScopePreviewState'

const props = defineProps<{
  kbId: string
  sourceId?: string
  storedSettings?: Record<string, any> | null
}>()

const settings = defineModel<Record<string, any>>({ required: true })
const { t, locale } = useI18n()
const repository = computed({
  get: () => String(settings.value.repository || ''),
  set: value => { settings.value = { ...settings.value, repository: value } },
})
const refName = computed({
  get: () => String(settings.value.ref || ''),
  set: value => { settings.value = { ...settings.value, ref: value } },
})
const paths = computed({
  get: () => Array.isArray(settings.value.paths) ? settings.value.paths.join('\n') : '',
  set: value => { settings.value = { ...settings.value, paths: value.split('\n').map(p => p.trim()).filter(Boolean) } },
})
const excludes = computed({
  get: () => Array.isArray(settings.value.exclude) ? settings.value.exclude.join('\n') : '',
  set: value => { settings.value = { ...settings.value, exclude: value.split('\n').map(p => p.trim()).filter(Boolean) } },
})

const loadingPreview = ref(false)
const previewError = ref('')
const preview = ref<GitHubDocumentScopePreview | null>(null)
let generation = 0

const repositoryOrRefChanged = computed(() => {
  if (!props.sourceId) return false
  return repository.value.trim().toLowerCase() !== String(props.storedSettings?.repository || '').trim().toLowerCase()
    || refName.value.trim() !== String(props.storedSettings?.ref || '').trim()
})
const canPreview = computed(() => !!props.sourceId && !repositoryOrRefChanged.value && settings.value.mode !== 'source')

watch(
  () => [props.sourceId, repository.value, refName.value, settings.value.mode,
    JSON.stringify(settings.value.paths), JSON.stringify(settings.value.exclude)],
  () => { generation += 1; preview.value = null; previewError.value = ''; loadingPreview.value = false },
)

function formatCount(value: number): string {
  return new Intl.NumberFormat(locale.value).format(value || 0)
}

function formatBytes(value: number): string {
  if (value < 1024) return `${formatCount(value)} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

const warningMessages = computed(() => {
  const data = preview.value
  if (!data) return []
  const key = 'datasource.githubBulk.scopePreview.'
  const warnings: string[] = []
  if (data.full_repository) warnings.push(t(key + 'fullRepo'))
  if (data.tree_state === 'truncated') warnings.push(t(key + 'truncated'))
  if (data.actual_sync.warnings.includes('document_count_limit')) warnings.push(t(key + 'countLimit'))
  if (data.actual_sync.warnings.includes('batch_limit_if_all_changed')) warnings.push(t(key + 'batchLimit'))
  if (data.actual_sync.parser_unsupported_files > 0) warnings.push(t(key + 'unsupportedWarning'))
  if (data.actual_sync.sensitive_candidate_files > 0) warnings.push(t(key + 'sensitiveSkipped', {
    count: formatCount(data.actual_sync.sensitive_candidate_files),
    bytes: formatBytes(data.actual_sync.sensitive_candidate_bytes),
  }))
  if (data.actual_sync.too_large_files > 0) warnings.push(t(key + 'fileLimit'))
  if (data.proposed_after_exclude) warnings.push(t(key + (data.exclusions_applied_by_sync ? 'excludeApplied' : 'excludeIgnored')))
  return warnings
})

async function loadPreview() {
  if (!canPreview.value || !props.sourceId) return
  const current = ++generation
  loadingPreview.value = true
  previewError.value = ''
  preview.value = null
  try {
    const response = await previewGitHubDocumentScope(props.kbId, githubScopePreviewRequest(props.sourceId, settings.value))
    if (current !== generation) return
    const wrapped = response as unknown as { data?: GitHubDocumentScopePreview }
    const data = (wrapped.data ?? response) as GitHubDocumentScopePreview
    if (canShowGitHubScopePreview(data)) preview.value = data
    else previewError.value = t(githubScopePreviewErrorKey(data))
  } catch (error: any) {
    if (current !== generation) return
    previewError.value = t(githubScopePreviewErrorKey(error || {}))
  } finally {
    if (current === generation) loadingPreview.value = false
  }
}
</script>

<template>
  <h4 class="setting-drawer__section-title">{{ t('datasource.github.repository') }}</h4>
  <p class="github-source-hint">{{ t(settings.mode === 'source' ? 'datasource.source.archiveHint' : 'datasource.github.hint') }}</p>
  <t-form label-align="top">
    <t-form-item :label="t('datasource.github.repository')" required>
      <t-input v-model="repository" placeholder="owner/repository" />
    </t-form-item>
    <t-form-item :label="t('datasource.gitlab.ref')">
      <t-input v-model="refName" :placeholder="t('datasource.gitlab.refPlaceholder')" />
    </t-form-item>
    <t-form-item :label="t('datasource.gitlab.paths')">
      <t-textarea v-model="paths" :placeholder="t('datasource.github.pathsHint')" :autosize="{ minRows: 3, maxRows: 7 }" />
    </t-form-item>
    <t-form-item v-if="settings.mode !== 'source'" :label="t('datasource.githubBulk.scopePreview.excludeLabel')">
      <t-textarea v-model="excludes" :placeholder="t('datasource.githubBulk.scopePreview.excludeHint')" :autosize="{ minRows: 2, maxRows: 6 }" />
      <p class="github-source-hint">{{ t('datasource.githubBulk.scopePreview.excludeHint') }}</p>
    </t-form-item>
  </t-form>
  <section v-if="settings.mode !== 'source'" class="github-scope-preview">
    <h5>{{ t('datasource.githubBulk.scopePreview.title') }}</h5>
    <p class="github-source-hint">{{ t('datasource.githubBulk.scopePreview.hint') }}</p>
    <t-alert v-if="!props.sourceId" theme="info" :message="t('datasource.githubBulk.scopePreview.existingOnly')" />
    <t-alert v-else-if="repositoryOrRefChanged" theme="warning" :message="t('datasource.githubBulk.scopePreview.repositoryChanged')" />
    <t-button v-else variant="outline" size="small" :loading="loadingPreview" @click="loadPreview">
      {{ t('datasource.githubBulk.scopePreview.action') }}
    </t-button>
    <t-alert v-if="previewError" theme="error" :message="previewError" class="github-preview-alert" />
    <div v-if="preview" class="github-preview-result" role="status">
      <p class="github-preview-commit">
        {{ t('datasource.githubBulk.scopePreview.commit') }}: <code>{{ preview.commit?.slice(0, 12) }}</code>
      </p>
      <p v-if="preview.paths_overridden" class="github-source-hint">{{ t('datasource.githubBulk.scopePreview.unsavedPaths') }}</p>
      <strong>{{ t('datasource.githubBulk.scopePreview.selectedScope') }}</strong>
      <div class="github-preview-stats">
        <span><b>{{ formatCount(preview.actual_sync.candidate_files) }}</b>{{ t('datasource.githubBulk.scopePreview.candidate') }}</span>
        <span><b>{{ formatBytes(preview.actual_sync.candidate_bytes) }}</b>{{ t('datasource.githubBulk.scopePreview.bytes') }}</span>
        <span><b>{{ formatCount(preview.actual_sync.image_files) }}</b>{{ t('datasource.githubBulk.scopePreview.images') }} · {{ formatBytes(preview.actual_sync.image_bytes) }}</span>
        <span v-if="preview.actual_sync.sensitive_candidate_files"><b>{{ formatCount(preview.actual_sync.sensitive_candidate_files) }}</b>{{ t('datasource.githubBulk.scopePreview.sensitiveCandidate') }} · {{ formatBytes(preview.actual_sync.sensitive_candidate_bytes) }}</span>
        <span><b>{{ formatCount(preview.actual_sync.user_excluded_files) }}</b>{{ t('datasource.githubBulk.scopePreview.excluded') }} · {{ formatBytes(preview.actual_sync.user_excluded_bytes) }}</span>
        <span><b>{{ formatCount(preview.actual_sync.parser_unsupported_files) }}</b>{{ t('datasource.githubBulk.scopePreview.unsupported') }}</span>
        <span><b>{{ formatCount(preview.actual_sync.too_large_files) }}</b>{{ t('datasource.githubBulk.scopePreview.tooLarge') }}</span>
      </div>
      <p class="github-preview-detail">
        {{ t('datasource.githubBulk.scopePreview.eligible') }}: {{ formatCount(preview.actual_sync.eligible_files) }} ·
        {{ t('datasource.githubBulk.scopePreview.extensions') }}:
        {{ preview.actual_sync.extensions.slice(0, 6).map(group => `${group.name} ${group.files}`).join(' · ') || '—' }}
      </p>
      <p v-if="preview.actual_sync.sample_paths.length" class="github-preview-detail">
        {{ t('datasource.githubBulk.scopePreview.sample') }}:
        {{ preview.actual_sync.sample_paths.slice(0, 5).join(' · ') }}
      </p>
      <div v-if="preview.proposed_after_exclude" class="github-preview-proposed">
        <strong>{{ t('datasource.githubBulk.scopePreview.proposedScope') }}</strong>
        <span>{{ t('datasource.githubBulk.scopePreview.candidate') }} {{ formatCount(preview.proposed_after_exclude.candidate_files) }} · {{ formatBytes(preview.proposed_after_exclude.candidate_bytes) }}</span>
        <span>{{ t('datasource.githubBulk.scopePreview.excluded') }} {{ formatCount(preview.proposed_after_exclude.user_excluded_files) }} · {{ formatBytes(preview.proposed_after_exclude.user_excluded_bytes) }}</span>
      </div>
      <t-alert v-if="warningMessages.length" theme="warning" :message="warningMessages.join(' ')" class="github-preview-alert" />
    </div>
  </section>
</template>

<style scoped>
.github-source-hint {
  color: var(--td-text-color-secondary);
  font-size: var(--td-font-size-body-small);
  line-height: 1.6;
  margin: 0 0 16px;
}
.github-scope-preview { margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--td-component-border); }
.github-scope-preview h5 { margin: 0 0 8px; font-size: var(--td-font-size-title-small); }
.github-preview-result { margin-top: 14px; display: grid; gap: 10px; color: var(--td-text-color-primary); }
.github-preview-commit, .github-preview-detail { margin: 0; overflow-wrap: anywhere; color: var(--td-text-color-secondary); font-size: var(--td-font-size-body-small); }
.github-preview-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 8px; }
.github-preview-stats span { display: grid; gap: 4px; padding: 9px; border: 1px solid var(--td-component-border); border-radius: var(--td-radius-default); font-size: var(--td-font-size-body-small); }
.github-preview-stats b { font-size: var(--td-font-size-title-small); }
.github-preview-proposed { display: flex; flex-wrap: wrap; gap: 8px; justify-content: space-between; padding: 9px; background: var(--td-bg-color-secondarycontainer); border-radius: var(--td-radius-default); }
.github-preview-alert { margin-top: 8px; }
</style>
