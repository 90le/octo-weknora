<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  createGitHubDataSourceBatch,
  discoverGitHubRepositories,
  type GitHubBatchResponse,
  type GitHubBatchResultItem,
  type GitHubBulkMode,
  type GitHubDiscoveryResponse,
  type GitHubRepository,
} from '@/api/datasource'
import SettingDrawer from '@/components/settings/SettingDrawer.vue'
import DataSourceTypeIcon from './DataSourceTypeIcon.vue'
import {
  defaultGitHubBulkSelection,
  filterGitHubRepositories,
  parseGitHubPaths,
  summarizeGitHubBulkResults,
  uniqueGitHubRepositories,
} from './githubBulkImportState'

const props = defineProps<{ kbId: string }>()
const visible = defineModel<boolean>('visible', { default: false })
const emit = defineEmits<{ saved: [] }>()
const { t } = useI18n()

type Step = 0 | 1 | 2

const step = ref<Step>(0)
const owner = ref('')
const accessToken = ref('')
const repositories = ref<GitHubRepository[]>([])
const selectedRepositoryNames = ref<string[]>([])
const search = ref('')
const includeArchived = ref(false)
const mode = ref<GitHubBulkMode>('source')
const pathsText = ref('')
const syncSchedule = ref('0 0 */6 * * *')
const startSync = ref(false)
const discovering = ref(false)
const submitting = ref(false)
const errorMessage = ref('')
const results = ref<GitHubBatchResultItem[]>([])

const stepTitles = computed(() => [
  t('datasource.githubBulk.steps.discover'),
  t('datasource.githubBulk.steps.configure'),
  t('datasource.githubBulk.steps.results'),
])

const scheduleOptions = computed(() => [
  { value: '', label: t('datasource.githubBulk.schedule.none') },
  { value: '0 0 */6 * * *', label: t('datasource.githubBulk.schedule.sixHours') },
  { value: '0 0 0 * * *', label: t('datasource.githubBulk.schedule.daily') },
  { value: '0 0 0 * * 1', label: t('datasource.githubBulk.schedule.weekly') },
])

const filteredRepositories = computed(() => filterGitHubRepositories(
  repositories.value,
  search.value,
  includeArchived.value,
))

const selectedRepositorySet = computed(() => new Set(selectedRepositoryNames.value))
const selectedRepositories = computed(() => repositories.value.filter(
  (repository) => selectedRepositorySet.value.has(repository.repository),
))
const selectedCount = computed(() => selectedRepositories.value.length)
const resultSummary = computed(() => summarizeGitHubBulkResults(results.value))

const drawerDescription = computed(() => {
  if (step.value === 0) return t('datasource.githubBulk.description')
  if (step.value === 1) return t('datasource.githubBulk.configureDescription')
  return t('datasource.githubBulk.resultsDescription')
})

const drawerConfirmText = computed(() => {
  if (step.value === 0) return t('datasource.githubBulk.discover')
  if (step.value === 1) return t('datasource.githubBulk.create')
  return t('common.close')
})

const confirmDisabled = computed(() => {
  if (step.value === 0) return !normalizeOwner(owner.value)
  if (step.value === 1) return selectedCount.value === 0
  return false
})

function normalizeOwner(value: string): string {
  return value
    .trim()
    .replace(/^https?:\/\/(?:www\.)?github\.com\//i, '')
    .replace(/^@/, '')
    .replace(/\/.+$/, '')
    .replace(/\/$/, '')
}

function credentials(): Record<string, unknown> | undefined {
  const token = accessToken.value.trim()
  return token ? { access_token: token } : undefined
}

function unwrapResponse<T>(response: unknown): T {
  const value = response as { data?: unknown } | undefined
  return (value?.data ?? response) as T
}

function readError(error: unknown): string {
  const value = error as { message?: string; error?: string; response?: { data?: { error?: string; message?: string } } }
  return value?.response?.data?.error || value?.response?.data?.message || value?.message || value?.error || t('datasource.githubBulk.operationFailed')
}

function reset() {
  step.value = 0
  owner.value = ''
  accessToken.value = ''
  repositories.value = []
  selectedRepositoryNames.value = []
  search.value = ''
  includeArchived.value = false
  mode.value = 'source'
  pathsText.value = ''
  syncSchedule.value = '0 0 */6 * * *'
  startSync.value = false
  discovering.value = false
  submitting.value = false
  errorMessage.value = ''
  results.value = []
}

watch(visible, (opened) => {
  if (opened) reset()
})

async function discover() {
  const normalizedOwner = normalizeOwner(owner.value)
  if (!normalizedOwner) return
  discovering.value = true
  errorMessage.value = ''
  try {
    const response = unwrapResponse<GitHubDiscoveryResponse>(
      await discoverGitHubRepositories(normalizedOwner, credentials()),
    )
    owner.value = response.owner || normalizedOwner
    repositories.value = uniqueGitHubRepositories(response.repositories || [])
    selectedRepositoryNames.value = defaultGitHubBulkSelection(repositories.value)
    search.value = ''
    includeArchived.value = false
    step.value = 1
    if (repositories.value.length === 0) {
      MessagePlugin.info(t('datasource.githubBulk.emptyDiscovery'))
    }
  } catch (error) {
    errorMessage.value = readError(error)
  } finally {
    discovering.value = false
  }
}

function selectVisible() {
  selectedRepositoryNames.value = Array.from(new Set([
    ...selectedRepositoryNames.value,
    ...filteredRepositories.value.map((repository) => repository.repository),
  ]))
}

function clearSelection() {
  selectedRepositoryNames.value = []
}

function backToDiscovery() {
  step.value = 0
  errorMessage.value = ''
}

async function createBatch() {
  if (selectedRepositories.value.length === 0) return
  submitting.value = true
  errorMessage.value = ''
  try {
    const response = unwrapResponse<GitHubBatchResponse>(await createGitHubDataSourceBatch({
      knowledge_base_id: props.kbId,
      owner: owner.value,
      repositories: selectedRepositories.value,
      credentials: credentials(),
      mode: mode.value,
      paths: parseGitHubPaths(pathsText.value),
      sync_schedule: syncSchedule.value || undefined,
      start_sync: startSync.value,
    }))
    results.value = response.results || []
    step.value = 2
    emit('saved')
    if (resultSummary.value.failed > 0) {
      MessagePlugin.warning(t('datasource.githubBulk.completedWithFailures'))
    } else {
      MessagePlugin.success(t('datasource.githubBulk.completed'))
    }
  } catch (error) {
    errorMessage.value = readError(error)
  } finally {
    submitting.value = false
  }
}

function close() {
  visible.value = false
}

async function handleConfirm() {
  if (step.value === 0) await discover()
  else if (step.value === 1) await createBatch()
  else close()
}

function resultTheme(status: GitHubBatchResultItem['status']) {
  if (status === 'created') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'existing') return 'warning'
  return 'default'
}

function resultStatusLabel(status: GitHubBatchResultItem['status']) {
  const keys = {
    created: 'datasource.githubBulk.status.created',
    existing: 'datasource.githubBulk.status.existing',
    failed: 'datasource.githubBulk.status.failed',
    skipped: 'datasource.githubBulk.status.skipped',
  } as const
  return t(keys[status])
}
</script>

<template>
  <SettingDrawer
    v-model:visible="visible"
    :title="t('datasource.githubBulk.title')"
    :description="drawerDescription"
    icon="logo-github"
    width="760px"
    storage-key="setting-drawer:width:github-bulk-import"
    :confirm-text="drawerConfirmText"
    :confirm-disabled="confirmDisabled"
    :confirm-loading="discovering || submitting"
    class="github-bulk-import-drawer"
    @confirm="handleConfirm"
    @cancel="reset"
  >
    <template #headerIcon>
      <DataSourceTypeIcon type="github" :size="20" />
    </template>

    <template #footer-left>
      <t-button v-if="step === 1" variant="outline" @click="backToDiscovery">
        {{ t('datasource.githubBulk.back') }}
      </t-button>
      <span v-if="step === 1" class="github-bulk-footer-count">
        {{ t('datasource.githubBulk.selectedCount', { count: selectedCount }) }}
      </span>
      <span v-if="step === 2" class="github-bulk-footer-count">
        {{ t('datasource.githubBulk.completedSummary', resultSummary) }}
      </span>
    </template>

    <div class="github-bulk-steps" aria-label="GitHub bulk import progress">
      <div
        v-for="(title, index) in stepTitles"
        :key="title"
        :class="['github-bulk-step', { active: step === index, done: step > index }]"
      >
        <span class="github-bulk-step__number">
          <t-icon v-if="step > index" name="check" />
          <template v-else>{{ index + 1 }}</template>
        </span>
        <span>{{ title }}</span>
      </div>
    </div>

    <t-alert v-if="errorMessage" theme="error" :message="errorMessage" close @close="errorMessage = ''" />

    <section v-if="step === 0" class="setting-drawer__section">
      <h4 class="setting-drawer__section-title">{{ t('datasource.githubBulk.ownerTitle') }}</h4>
      <p class="github-bulk-hint">{{ t('datasource.githubBulk.ownerHint') }}</p>
      <t-form label-align="top">
        <t-form-item :label="t('datasource.githubBulk.ownerLabel')" required>
          <t-input
            v-model="owner"
            :placeholder="t('datasource.githubBulk.ownerPlaceholder')"
            autocomplete="off"
            @enter="discover"
          />
        </t-form-item>
        <t-form-item :label="t('datasource.githubBulk.tokenLabel')">
          <t-input
            v-model="accessToken"
            type="password"
            :placeholder="t('datasource.githubBulk.tokenPlaceholder')"
            autocomplete="off"
          >
            <template #prefix-icon><t-icon name="lock-on" /></template>
          </t-input>
          <p class="github-bulk-field-hint">{{ t('datasource.githubBulk.tokenHint') }}</p>
        </t-form-item>
      </t-form>
      <t-alert theme="info" :message="t('datasource.githubBulk.discoveryNotice')" />
    </section>

    <section v-else-if="step === 1" class="setting-drawer__section github-bulk-selection-section">
      <div class="github-bulk-selection-header">
        <div>
          <h4 class="setting-drawer__section-title">{{ t('datasource.githubBulk.repositoryTitle') }}</h4>
          <p class="github-bulk-hint">{{ t('datasource.githubBulk.repositoryHint', { owner }) }}</p>
        </div>
        <span class="github-bulk-selection-total">{{ t('datasource.githubBulk.discoveredCount', { count: repositories.length }) }}</span>
      </div>

      <div class="github-bulk-filter-row">
        <t-input v-model="search" clearable :placeholder="t('datasource.githubBulk.searchPlaceholder')">
          <template #prefix-icon><t-icon name="search" /></template>
        </t-input>
        <t-checkbox v-model="includeArchived">{{ t('datasource.githubBulk.includeArchived') }}</t-checkbox>
      </div>

      <div class="github-bulk-selection-actions">
        <t-button variant="text" size="small" @click="selectVisible">
          {{ t('datasource.githubBulk.selectVisible') }}
        </t-button>
        <span aria-hidden="true">·</span>
        <t-button variant="text" size="small" @click="clearSelection">
          {{ t('datasource.githubBulk.clearSelection') }}
        </t-button>
      </div>

      <div v-if="filteredRepositories.length" class="github-bulk-repository-list">
        <t-checkbox-group v-model="selectedRepositoryNames" class="github-bulk-repository-group">
          <label
            v-for="repository in filteredRepositories"
            :key="repository.repository"
            class="github-bulk-repository-row"
            :class="{ 'is-selected': selectedRepositorySet.has(repository.repository), archived: repository.archived }"
          >
            <t-checkbox :value="repository.repository" />
            <span class="github-bulk-repository-row__body">
              <span class="github-bulk-repository-row__title">
                <strong>{{ repository.repository }}</strong>
                <t-tag v-if="repository.archived" size="small" theme="warning" variant="light">
                  {{ t('datasource.githubBulk.archived') }}
                </t-tag>
              </span>
              <span v-if="repository.description" class="github-bulk-repository-row__description">
                {{ repository.description }}
              </span>
              <span class="github-bulk-repository-row__meta">
                {{ t('datasource.githubBulk.defaultBranch', { branch: repository.default_branch || '--' }) }}
              </span>
            </span>
          </label>
        </t-checkbox-group>
      </div>
      <t-empty v-else :description="t('datasource.githubBulk.noMatches')" />
    </section>

    <section v-else class="setting-drawer__section">
      <h4 class="setting-drawer__section-title">{{ t('datasource.githubBulk.resultsTitle') }}</h4>
      <p class="github-bulk-hint">{{ t('datasource.githubBulk.resultsHint') }}</p>
      <div class="github-bulk-result-summary">
        <span class="created">{{ t('datasource.githubBulk.resultCreated', { count: resultSummary.created }) }}</span>
        <span class="existing">{{ t('datasource.githubBulk.resultExisting', { count: resultSummary.existing }) }}</span>
        <span class="failed">{{ t('datasource.githubBulk.resultFailed', { count: resultSummary.failed }) }}</span>
      </div>
      <div v-if="results.length" class="github-bulk-result-list">
        <div v-for="result in results" :key="result.repository" class="github-bulk-result-row">
          <div>
            <strong>{{ result.repository }}</strong>
            <p v-if="result.message">{{ result.message }}</p>
          </div>
          <t-tag :theme="resultTheme(result.status)" variant="light">
            {{ resultStatusLabel(result.status) }}
          </t-tag>
        </div>
      </div>
      <t-empty v-else :description="t('datasource.githubBulk.noResults')" />
    </section>

    <section v-if="step === 1" class="setting-drawer__section">
      <h4 class="setting-drawer__section-title">{{ t('datasource.githubBulk.syncTitle') }}</h4>
      <div class="github-bulk-config-grid">
        <div>
          <label class="github-bulk-label">{{ t('datasource.source.usage') }}</label>
          <t-radio-group v-model="mode">
            <t-radio value="source">{{ t('datasource.source.readonly') }}</t-radio>
            <t-radio value="documents">{{ t('datasource.source.documents') }}</t-radio>
          </t-radio-group>
          <p class="github-bulk-field-hint">{{ t(mode === 'source' ? 'datasource.githubBulk.sourceModeHint' : 'datasource.githubBulk.documentsModeHint') }}</p>
        </div>
        <div>
          <label class="github-bulk-label">{{ t('datasource.syncScheduleLabel') }}</label>
          <t-select v-model="syncSchedule">
            <t-option v-for="option in scheduleOptions" :key="option.value" :value="option.value" :label="option.label" />
          </t-select>
        </div>
      </div>
      <div>
        <label class="github-bulk-label">{{ t('datasource.gitlab.paths') }}</label>
        <t-textarea
          v-model="pathsText"
          :placeholder="t('datasource.github.pathsHint')"
          :autosize="{ minRows: 3, maxRows: 6 }"
        />
        <p class="github-bulk-field-hint">{{ t('datasource.githubBulk.pathsHint') }}</p>
      </div>
      <t-checkbox v-model="startSync">{{ t('datasource.githubBulk.startSync') }}</t-checkbox>
      <p class="github-bulk-field-hint">{{ t('datasource.githubBulk.startSyncHint') }}</p>
    </section>
  </SettingDrawer>
</template>

<style scoped lang="less">
@import './datasource-surface.less';

.github-bulk-steps {
  display: flex;
  gap: 8px;
  margin-bottom: 20px;
  border-bottom: 1px solid var(--td-component-stroke);
  padding-bottom: 14px;
}

.github-bulk-step {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  color: var(--td-text-color-placeholder);
  font-size: 13px;

  &.active { color: var(--td-brand-color); font-weight: 500; }
  &.done { color: var(--td-text-color-secondary); }
}

.github-bulk-step__number {
  display: inline-flex;
  width: 22px;
  height: 22px;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  border: 1px solid var(--td-component-stroke);
  border-radius: 50%;
  font-size: 12px;
  font-weight: 600;
}

.github-bulk-step.active .github-bulk-step__number {
  border-color: var(--td-brand-color);
  background: var(--td-brand-color);
  color: #fff;
}

.github-bulk-step.done .github-bulk-step__number {
  border-color: transparent;
  background: color-mix(in srgb, var(--td-brand-color) 12%, transparent);
  color: var(--td-brand-color);
}

.github-bulk-hint,
.github-bulk-field-hint {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 1.6;
}

.github-bulk-field-hint { margin-top: 6px; color: var(--td-text-color-placeholder); }
.github-bulk-label { display: block; margin-bottom: 8px; color: var(--td-text-color-primary); font-size: 13px; font-weight: 500; }
.github-bulk-footer-count { overflow: hidden; color: var(--td-text-color-secondary); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }

.github-bulk-selection-section { min-height: 0; }
.github-bulk-selection-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.github-bulk-selection-total { flex-shrink: 0; color: var(--td-text-color-placeholder); font-size: 12px; padding-top: 3px; }
.github-bulk-filter-row { display: flex; align-items: center; gap: 12px; }
.github-bulk-filter-row > :first-child { flex: 1; }
.github-bulk-filter-row :deep(.t-checkbox) { flex-shrink: 0; }
.github-bulk-selection-actions { display: flex; align-items: center; gap: 4px; margin-top: -4px; color: var(--td-text-color-placeholder); }
.github-bulk-selection-actions :deep(.t-button) { padding: 0 4px; }

.github-bulk-repository-list {
  .ds-inset-panel();
  max-height: min(48vh, 460px);
  overflow: auto;
  padding: 6px;
}

.github-bulk-repository-group { display: flex; flex-direction: column; gap: 2px; }

.github-bulk-repository-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px;
  border-radius: 7px;
  cursor: pointer;
  transition: background 0.12s ease;

  &:hover, &.is-selected { background: var(--td-bg-color-container); }
  &.archived { opacity: 0.72; }
  :deep(.t-checkbox) { margin-top: 2px; }
}

.github-bulk-repository-row__body { display: flex; min-width: 0; flex: 1; flex-direction: column; gap: 3px; }
.github-bulk-repository-row__title { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; min-width: 0; color: var(--td-text-color-primary); font-size: 13px; }
.github-bulk-repository-row__description { overflow: hidden; color: var(--td-text-color-secondary); font-size: 12px; line-height: 1.45; text-overflow: ellipsis; white-space: nowrap; }
.github-bulk-repository-row__meta { color: var(--td-text-color-placeholder); font-size: 11px; }

.github-bulk-config-grid { display: grid; grid-template-columns: minmax(0, 1fr) 210px; gap: 16px; }

.github-bulk-result-summary { display: flex; flex-wrap: wrap; gap: 8px; font-size: 12px; }
.github-bulk-result-summary span { padding: 4px 8px; border-radius: 5px; background: var(--td-bg-color-secondarycontainer); }
.github-bulk-result-summary .created { color: var(--td-success-color); }
.github-bulk-result-summary .existing { color: var(--td-warning-color); }
.github-bulk-result-summary .failed { color: var(--td-error-color); }
.github-bulk-result-list { display: flex; flex-direction: column; gap: 4px; max-height: min(48vh, 460px); overflow: auto; }
.github-bulk-result-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; padding: 10px; border-bottom: 1px solid var(--td-component-stroke); }
.github-bulk-result-row strong { color: var(--td-text-color-primary); font-size: 13px; }
.github-bulk-result-row p { margin: 4px 0 0; color: var(--td-text-color-secondary); font-size: 12px; line-height: 1.5; }

@media (max-width: 640px) {
  .github-bulk-steps { gap: 4px; }
  .github-bulk-step { gap: 5px; font-size: 11px; }
  .github-bulk-step__number { width: 20px; height: 20px; }
  .github-bulk-selection-header, .github-bulk-filter-row { align-items: stretch; flex-direction: column; }
  .github-bulk-config-grid { grid-template-columns: 1fr; }
}
</style>
